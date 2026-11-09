package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"

	"rss-scraper/internal/feedio"
	"rss-scraper/internal/model"
)

// onDemandItemLimit caps how many articles the on-demand endpoint extracts
// inline (spec: up to ~10 newest).
const onDemandItemLimit = 10

// onDemandOverallBudget bounds the whole on-demand request; per-item work
// inherits the shrinking deadline (spec: serve partially processed feed).
const onDemandOverallBudget = 45 * time.Second

// handleFeedToken serves /feed/<token>.xml (the .xml suffix is optional).
// No session auth: the 32-hex token is the credential (NFR3/AC6); unknown
// tokens get a bare 404 with no hints.
func (s *Server) handleFeedToken(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	token = strings.TrimSuffix(token, ".xml")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	feed, err := s.st.FeedByToken(r.Context(), token)
	if err != nil {
		// Missing token and store failure look identical from outside.
		http.NotFound(w, r)
		return
	}

	items, err := s.st.ItemsForFeed(r.Context(), feed.ID, 100)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	body, _, err := s.renderStoredFeed(feed, items)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}

	etag := `"` + etagOf(body) + `"`
	if etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", feedio.ContentType())
	w.Header().Set("ETag", etag)
	lastMod := lastModifiedOf(feed)
	if !lastMod.IsZero() {
		w.Header().Set("Last-Modified", lastMod.UTC().Format(http.TimeFormat))
	}
	_, _ = w.Write(body)
}

// renderStoredFeed assembles the RSS output for a saved feed from cached
// items. Extracted items carry their cleaned HTML; the rest fall back to
// the original summary (feedio adds the fallback marker).
func (s *Server) renderStoredFeed(feed *model.Feed, items []model.Item) ([]byte, time.Time, error) {
	title := feed.Title
	if title == "" {
		title = "Saved feed " + strconv.FormatInt(feed.ID, 10)
	}
	out := feedio.Output{
		Title:        title,
		Link:         feed.SourceURL,
		Description:  "Full-text feed",
		LastModified: lastModifiedOf(feed),
		Items:        make([]feedio.ItemContent, 0, len(items)),
	}
	for i := range items {
		it := &items[i]
		gi := gofeedItemFromStored(it)
		html := it.ContentHTML
		fallback := !it.Extracted
		if html == "" && it.Extracted {
			// Extracted but empty (edge case): fall back to summary too.
			html = it.Summary
			fallback = true
		}
		out.Items = append(out.Items, feedio.ItemContent{Item: gi, HTML: html, Fallback: fallback})
	}
	body, err := feedio.ToRSS(out)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("render feed %d: %w", feed.ID, err)
	}
	return body, lastModifiedOf(feed), nil
}

// gofeedItemFromStored rebuilds the gofeed item shape feedio serializes.
func gofeedItemFromStored(it *model.Item) *gofeed.Item {
	gi := &gofeed.Item{
		Title:       it.Title,
		Link:        it.URL,
		Description: it.Summary,
		GUID:        it.GUID,
		Categories:  it.Categories,
	}
	if it.Author != "" {
		gi.Author = &gofeed.Person{Name: it.Author}
	}
	if it.PublishedAt.Valid {
		t := it.PublishedAt.Time
		gi.PublishedParsed = &t
	}
	return gi
}

// lastModifiedOf picks the feed's Last-Modified timestamp.
func lastModifiedOf(feed *model.Feed) time.Time {
	if feed.LastRefresh.Valid {
		return feed.LastRefresh.Time
	}
	return feed.CreatedAt
}

// etagOf is a short sha256 hex digest of the body.
func etagOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:16])
}

// etagMatch implements weak If-None-Match comparison ("*", exact, W/).
func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	if strings.TrimSpace(header) == "*" {
		return true
	}
	for _, cand := range strings.Split(header, ",") {
		cand = strings.TrimSpace(cand)
		cand = strings.TrimPrefix(cand, "W/")
		if cand == etag {
			return true
		}
	}
	return false
}

// handleOnDemandPage renders the on-demand UI page.
func (s *Server) handleOnDemandPage(w http.ResponseWriter, r *http.Request) (any, error) {
	return struct{}{}, nil
}

// handleOnDemand converts an arbitrary feed URL on the fly: fetch + parse
// the source, extract up to 10 newest items inline, serve RSS immediately.
// Nothing is persisted. Slow or failing items keep their summary; the
// overall budget caps total work (spec section 7).
func (s *Server) handleOnDemand(w http.ResponseWriter, r *http.Request) {
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	if rawURL == "" {
		http.Error(w, "missing url parameter", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		http.Error(w, "url must be an http(s) feed URL", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), onDemandOverallBudget)
	defer cancel()

	// The source fetch is NOT rate-limited through the article fetcher's
	// per-host limiter twice; Fetch applies spacing per host already.
	body, err := s.fetcher.Fetch(ctx, rawURL)
	if err != nil {
		http.Error(w, "fetch source feed: "+err.Error(), http.StatusBadGateway)
		return
	}
	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		http.Error(w, "parse feed: "+err.Error(), http.StatusBadGateway)
		return
	}

	limit := len(parsed.Items)
	if limit > onDemandItemLimit {
		limit = onDemandItemLimit
	}
	out := feedio.Output{
		Title:        firstNonEmpty(parsed.Title, "On-demand feed"),
		Link:         firstNonEmpty(parsed.Link, rawURL),
		Description:  "Full-text feed",
		LastModified: time.Now(),
		Items:        make([]feedio.ItemContent, 0, limit),
	}
	for _, gi := range parsed.Items[:limit] {
		out.Items = append(out.Items, s.onDemandItem(ctx, gi))
	}

	xml, err := feedio.ToRSS(out)
	if err != nil {
		http.Error(w, "render feed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", feedio.ContentType())
	// Short cache: readers re-poll on-demand URLs too.
	w.Header().Set("Cache-Control", "max-age=300")
	_, _ = w.Write(xml)
}

// onDemandItem extracts one article inline. Extraction failure, timeout, or
// empty result falls back to the original summary (Fallback=true).
func (s *Server) onDemandItem(ctx context.Context, gi *gofeed.Item) feedio.ItemContent {
	articleURL := strings.TrimSpace(gi.Link)
	if articleURL == "" {
		return feedio.ItemContent{Item: gi, HTML: gi.Description, Fallback: true}
	}
	res, err := s.extractor.Extract(ctx, articleURL)
	if err != nil || strings.TrimSpace(res.ContentHTML) == "" {
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			log.Printf("on-demand: extract %s: %v", articleURL, err)
		}
		return feedio.ItemContent{Item: gi, HTML: gi.Description, Fallback: true}
	}
	return feedio.ItemContent{Item: gi, HTML: res.ContentHTML}
}

// handleFeedRefresh runs a refresh cycle for one feed (manual button). The
// scheduler is used when main wired it in; otherwise a minimal inline
// refresh runs in a detached context so an aborted POST cannot strand the
// feed half-refreshed.
func (s *Server) handleFeedRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := feedPathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := s.st.Feed(r.Context(), id); err != nil {
		http.NotFound(w, r)
		return
	}
	var err error
	if s.refresher != nil {
		err = s.refresher.RefreshNow(r.Context(), id)
	} else {
		ctx, cancel := refreshCtx()
		defer cancel()
		err = s.inlineRefresh(ctx, id)
	}
	if err != nil {
		log.Printf("server: refresh feed %d: %v", id, err)
	}
	http.Redirect(w, r, fmt.Sprintf("/admin/feeds/%d", id), http.StatusSeeOther)
}

// inlineRefresh is the minimal per-feed pipeline used until main wires the
// scheduler in as Refresher: fetch source, upsert items, extract new ones,
// mark refreshed. Mirrors scheduler.processFeed semantics (new items only).
func (s *Server) inlineRefresh(ctx context.Context, feedID int64) error {
	feed, err := s.st.Feed(ctx, feedID)
	if err != nil {
		return fmt.Errorf("load feed: %w", err)
	}
	body, err := s.fetcher.Fetch(ctx, feed.SourceURL)
	if err != nil {
		return s.markRefreshFailed(ctx, feedID, err)
	}
	parsed, err := gofeed.NewParser().ParseString(string(body))
	if err != nil {
		return s.markRefreshFailed(ctx, feedID, fmt.Errorf("parse feed: %w", err))
	}

	for _, gi := range parsed.Items {
		item, guid := storedItemFromFeed(gi)
		if guid == "" {
			continue // no guid and no link: nothing to dedupe on
		}
		isNew, err := s.st.UpsertItem(ctx, feedID, item)
		if err != nil {
			return s.markRefreshFailed(ctx, feedID, fmt.Errorf("upsert item: %w", err))
		}
		if !isNew {
			continue // already-seen items are never re-fetched (spec 7)
		}
		s.inlineExtract(ctx, item)
	}
	if err := s.st.MarkFeedRefreshed(ctx, feedID, "ok", "", time.Now()); err != nil {
		return fmt.Errorf("mark refreshed: %w", err)
	}
	return nil
}

// inlineExtract fetches+extracts one new item and stores the outcome.
func (s *Server) inlineExtract(ctx context.Context, item *model.Item) {
	if item.URL == "" {
		_ = s.st.UpdateItemContent(ctx, item.ID, "", false, true)
		return
	}
	res, err := s.extractor.Extract(ctx, item.URL)
	switch {
	case err == nil && strings.TrimSpace(res.ContentHTML) != "":
		_ = s.st.UpdateItemContent(ctx, item.ID, res.ContentHTML, true, false)
	default:
		_ = s.st.UpdateItemContent(ctx, item.ID, "", false, true)
	}
}

// markRefreshFailed records a failed cycle and returns the error.
func (s *Server) markRefreshFailed(ctx context.Context, feedID int64, err error) error {
	if ctx.Err() == nil {
		_ = s.st.MarkFeedRefreshed(ctx, feedID, "error", err.Error(), time.Now())
	}
	return err
}

// storedItemFromFeed maps a gofeed item to a store item (guid falls back to
// link; empty guid string means "skip").
func storedItemFromFeed(gi *gofeed.Item) (*model.Item, string) {
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

// firstNonEmpty returns s when non-empty, else alt.
func firstNonEmpty(s, alt string) string {
	if s != "" {
		return s
	}
	return alt
}
