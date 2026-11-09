// Package scheduler refreshes saved feeds in the background: a single loop
// ticks on an interval, claims due feeds from the store, and refreshes each
// feed in its own goroutine (bounded concurrency). For every NEW item the
// full article is fetched and extracted; known items are served from the
// store cache and never re-fetched.
//
// Article fetches go through the shared extract.Fetcher, whose per-host
// limiter (spacing + jitter + User-Agent) naturally spaces out extraction
// requests between items of the same host, including across concurrently
// refreshing feeds.
package scheduler

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/mmcdole/gofeed"

	"rss-scraper/internal/config"
	"rss-scraper/internal/extract"
	"rss-scraper/internal/model"
	"rss-scraper/internal/store"
)

const (
	// DefaultInterval is how often the loop looks for due feeds.
	DefaultInterval = time.Minute
	// DefaultConcurrency caps how many feeds are refreshed simultaneously.
	DefaultConcurrency = 3
)

// Scheduler periodically refreshes saved feeds. One instance is shared by
// the whole process; its Fetcher (and therefore the per-host rate limiter)
// is shared by all feed cycles.
type Scheduler struct {
	cfg       *config.Config
	st        *store.Store // also passed as extract.RuleSource via RuleForHost
	fetcher   *extract.Fetcher
	extractor *extract.Extractor

	// interval is the tick period; tests may set it before Start.
	interval time.Duration
	// concurrency caps simultaneous feed refreshes; tests may set it before
	// Start together with rebuilding sem.
	concurrency int

	sem chan struct{}

	// inFlight tracks feeds whose refresh cycle is currently running so a
	// tick landing mid-cycle does not claim the same feed again (last_refresh
	// is only written when the cycle ends).
	inFlightMu sync.Mutex
	inFlight   map[int64]bool

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds a Scheduler. The tick interval defaults to DefaultInterval and
// the per-feed concurrency to DefaultConcurrency. The store doubles as the
// rule source for extraction (its RuleForHost satisfies extract.RuleSource).
func New(cfg *config.Config, st *store.Store) *Scheduler {
	fetcher := extract.NewFetcher(cfg)
	return &Scheduler{
		cfg:         cfg,
		st:          st,
		fetcher:     fetcher,
		extractor:   extract.NewExtractor(fetcher, st),
		interval:    DefaultInterval,
		concurrency: DefaultConcurrency,
		sem:         make(chan struct{}, DefaultConcurrency),
		inFlight:    make(map[int64]bool),
	}
}

// Start launches the refresh loop and returns immediately. The loop makes an
// initial pass right away (so never-refreshed feeds are picked up on
// startup), then once per interval. Start must be called at most once per
// Scheduler; a second call is ignored defensively, and Start after Stop is
// also ignored. Use Stop to release the loop.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.loop(ctx)
	}()
}

// loop ticks on s.interval, processing all due feeds each tick.
func (s *Scheduler) loop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.processOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.processOnce(ctx)
		}
	}
}

// Stop cancels the loop and waits for in-flight feed cycles to finish.
// Requests still in flight fail fast against the canceled context, so the
// wait is short; a feed interrupted mid-cycle keeps last_refresh untouched
// and is simply picked up on the next run. Stop is safe even if Start was
// never called, and is idempotent.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
}

// processOnce claims all due feeds and refreshes each in its own goroutine,
// bounded by the concurrency semaphore. It returns as soon as the feed
// goroutines are spawned; tests can call waitIdle to await completion.
func (s *Scheduler) processOnce(ctx context.Context) {
	feeds, err := s.st.DueFeeds(ctx, time.Now())
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("scheduler: due feeds: %v", err)
		}
		return
	}
	for _, f := range feeds {
		f := f
		if !s.claimFeed(f.ID) {
			continue // cycle for this feed already running
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.releaseFeed(f.ID)
			// A panic in one feed cycle must not take down the process.
			defer func() {
				if p := recover(); p != nil {
					log.Printf("scheduler: feed %d: panic recovered: %v", f.ID, p)
				}
			}()
			select {
			case <-ctx.Done():
				return
			case s.sem <- struct{}{}:
				defer func() { <-s.sem }()
			}
			s.processFeed(ctx, f)
		}()
	}
}

// claimFeed marks a feed as being refreshed; false means a cycle for it is
// already in flight and this tick must not start a second one.
func (s *Scheduler) claimFeed(id int64) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if s.inFlight[id] {
		return false
	}
	s.inFlight[id] = true
	return true
}

// releaseFeed clears the in-flight marker after a cycle ends.
func (s *Scheduler) releaseFeed(id int64) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	delete(s.inFlight, id)
}

// waitIdle blocks until all spawned feed cycles have finished (and the loop
// goroutine, if Start was called). Test helper.
func (s *Scheduler) waitIdle() { s.wg.Wait() }

// processFeed performs one refresh cycle for a feed: fetch and parse the
// source, upsert every item, extract full text for new items only, then mark
// the feed refreshed. A fetch/parse failure marks the feed "error"; the
// previously cached items remain served from the store, so the output feed
// keeps working with the last good data.
func (s *Scheduler) processFeed(ctx context.Context, f model.Feed) {
	body, err := s.fetcher.Fetch(ctx, f.SourceURL)
	if err != nil {
		s.markRefreshFailed(ctx, f.ID, err)
		return
	}
	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		s.markRefreshFailed(ctx, f.ID, fmt.Errorf("parse feed: %w", err))
		return
	}

	for _, gi := range parsed.Items {
		item, guid := itemFromFeed(gi)
		if guid == "" {
			log.Printf("scheduler: feed %d: skipping item %q: no guid or link", f.ID, gi.Title)
			continue
		}
		isNew, err := s.st.UpsertItem(ctx, f.ID, item)
		if err != nil {
			log.Printf("scheduler: feed %d item %q: upsert: %v", f.ID, guid, err)
			continue
		}
		if !isNew {
			// Fetch scope: already-seen items are never re-fetched or
			// re-extracted; their cached content is served indefinitely.
			continue
		}
		// Extraction happens exactly once per item, right after its first
		// upsert. Failures are recorded failed=true and stay failed in this
		// version: store.PendingItems exposes them, but this scheduler does
		// not retry already-seen articles.
		s.extractItem(ctx, f.ID, item)
	}

	if err := s.st.MarkFeedRefreshed(ctx, f.ID, "ok", "", time.Now()); err != nil && ctx.Err() == nil {
		log.Printf("scheduler: feed %d: mark refreshed: %v", f.ID, err)
	}
}

// extractItem fetches and extracts the full text of one new item and stores
// the outcome. Any extraction error (ErrExtract incl. timeouts, rate-limit
// responses, or anything else) marks the item failed instead of crashing or
// stalling the cycle; the item keeps its original summary as fallback at
// serve time.
func (s *Scheduler) extractItem(ctx context.Context, feedID int64, item *model.Item) {
	if item.URL == "" {
		if ctx.Err() == nil {
			log.Printf("scheduler: feed %d item %q: no link; marking failed", feedID, item.GUID)
		}
		s.setContent(ctx, feedID, item, "", false, true)
		return
	}
	res, err := s.extractor.Extract(ctx, item.URL)
	switch {
	case err == nil && strings.TrimSpace(res.ContentHTML) != "":
		// Full text extracted; fetched_at is set by the store.
		s.setContent(ctx, feedID, item, res.ContentHTML, true, false)
	case err == nil:
		// Extraction "succeeded" but produced empty HTML: treat as failed.
		s.setContent(ctx, feedID, item, "", false, true)
	default:
		if ctx.Err() == nil {
			log.Printf("scheduler: feed %d item %q: extraction failed: %v", feedID, item.GUID, err)
		}
		s.setContent(ctx, feedID, item, "", false, true)
	}
}

// setContent persists the extraction outcome, quieting store errors during
// shutdown (the canceled context makes them expected).
func (s *Scheduler) setContent(ctx context.Context, feedID int64, item *model.Item, html string, extracted, failed bool) {
	if err := s.st.UpdateItemContent(ctx, item.ID, html, extracted, failed); err != nil && ctx.Err() == nil {
		log.Printf("scheduler: feed %d item %q: store content: %v", feedID, item.GUID, err)
	}
}

// markRefreshFailed records a source feed fetch/parse failure. It is skipped
// during shutdown so the feed stays due and is retried on the next run.
func (s *Scheduler) markRefreshFailed(ctx context.Context, feedID int64, err error) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("scheduler: feed %d: refresh failed: %v", feedID, err)
	if merr := s.st.MarkFeedRefreshed(ctx, feedID, "error", err.Error(), time.Now()); merr != nil {
		log.Printf("scheduler: feed %d: mark refreshed: %v", feedID, merr)
	}
}

// itemFromFeed maps a gofeed item to a store item. The guid falls back to the
// link when the feed omits <guid> (both are stable per-item identifiers);
// items with neither are skipped by the caller.
func itemFromFeed(gi *gofeed.Item) (*model.Item, string) {
	guid := strings.TrimSpace(gi.GUID)
	if guid == "" {
		guid = strings.TrimSpace(gi.Link)
	}
	item := &model.Item{
		GUID:       guid,
		URL:        strings.TrimSpace(gi.Link),
		Title:      strings.TrimSpace(gi.Title),
		Summary:    gi.Description,
		Categories: gi.Categories,
	}
	if gi.Author != nil && gi.Author.Name != "" {
		item.Author = gi.Author.Name
	} else if len(gi.Authors) > 0 && gi.Authors[0] != nil {
		item.Author = gi.Authors[0].Name
	}
	if gi.PublishedParsed != nil {
		item.PublishedAt = sql.NullTime{Time: *gi.PublishedParsed, Valid: true}
	}
	return item, guid
}
