package scheduler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"

	"rss-scraper/internal/config"
	"rss-scraper/internal/model"
	"rss-scraper/internal/store"
)

// testConfig returns a Config with low-latency fetch settings for tests.
func testConfig() *config.Config {
	return &config.Config{
		FetchTimeout: 5 * time.Second,
		FetchSpacing: 20 * time.Millisecond,
		FetchJitter:  5 * time.Millisecond,
		UserAgent:    "scheduler-test/1.0",
	}
}

// testStore opens a store in a fresh temp dir, failing the test on error.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// recordingServer is an httptest server serving a fixture RSS feed plus the
// article pages it links. It records every hit path with a timestamp so
// tests can assert on fetch counts and spacing. Hit recording is fully
// synchronized.
type recordingServer struct {
	*httptest.Server

	mu    sync.Mutex
	hits  []hit
	types map[string]string // path -> content type served
}

type hit struct {
	path string
	at   time.Time
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	rs := &recordingServer{types: map[string]string{}}

	const cleanHTML = `<!DOCTYPE html>
<html><head><title>Clean Article</title></head>
<body>
<nav><a href="/">Home</a> | <a href="/about">About</a> | Nav cruft text here.</nav>
<article>
  <h1>Clean Article</h1>
  <p>This is the full body text of the clean article, with unique markers.</p>
  <p>Second paragraph continues the story.</p>
</article>
<footer>footer junk</footer>
</body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
<title>Test Feed</title>
<link>%s</link>
<description>test</description>
<item>
  <title>Clean Article</title>
  <link>%s/clean.html</link>
  <guid>guid-clean</guid>
  <pubDate>Mon, 09 Nov 2026 06:00:00 GMT</pubDate>
  <description>Clean summary.</description>
</item>
<item>
  <title>Broken Article</title>
  <link>%s/missing.html</link>
  <guid>guid-broken</guid>
  <pubDate>Mon, 09 Nov 2026 05:00:00 GMT</pubDate>
  <description>Broken summary.</description>
</item>
</channel></rss>`, rs.URL, rs.URL, rs.URL)
	})
	mux.HandleFunc("/clean.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, cleanHTML)
	})
	// Any other path (e.g. /missing.html) is a 404.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	rs.types["/feed.xml"] = "application/rss+xml"

	rs.Server = httptest.NewServer(mux)
	rs.Server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits = append(rs.hits, hit{path: r.URL.Path, at: time.Now()})
		rs.mu.Unlock()
		mux.ServeHTTP(w, r)
	})
	t.Cleanup(rs.Close)
	return rs
}

// hitTimes returns the timestamps of all recorded hits to path.
func (rs *recordingServer) hitTimes(path string) []time.Time {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	var out []time.Time
	for _, h := range rs.hits {
		if h.path == path {
			out = append(out, h.at)
		}
	}
	return out
}

// articleHitCount returns how many times any article page (feed + articles
// pattern for the scheduler test) has been fetched.
func (rs *recordingServer) totalHits() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.hits)
}

// newTestScheduler builds a scheduler wired to st with tiny intervals.
func newTestScheduler(cfg *config.Config, st *store.Store) *Scheduler {
	s := New(cfg, st)
	s.interval = 50 * time.Millisecond
	s.concurrency = 3
	s.sem = make(chan struct{}, s.concurrency)
	return s
}

// feedView reads back the feed row for assertions.
func feedView(t *testing.T, st *store.Store, id int64) *model.Feed {
	t.Helper()
	f, err := st.Feed(context.Background(), id)
	if err != nil {
		t.Fatalf("store feed %d: %v", id, err)
	}
	return f
}

func TestProcessOnceExtractsNewItemsOnly(t *testing.T) {
	ctx := context.Background()
	rs := newRecordingServer(t)
	st := testStore(t)

	feedID, err := st.CreateFeed(ctx, &model.Feed{
		SourceURL:    rs.URL + "/feed.xml",
		RefreshEvery: time.Hour, // always due for manual processOnce calls
	})
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	s := newTestScheduler(testConfig(), st)
	s.processOnce(ctx) // spawns feed goroutines
	s.waitIdle()       // wait for the cycle to finish

	// Feed marked ok.
	f := feedView(t, st, feedID)
	if f.LastStatus != "ok" {
		t.Fatalf("feed last_status = %q, want ok (last_error %q)", f.LastStatus, f.LastError)
	}
	if !f.LastRefresh.Valid {
		t.Fatal("feed last_refresh not set")
	}

	items, err := st.ItemsForFeed(ctx, feedID, 10)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}

	byGUID := map[string]model.Item{}
	for _, it := range items {
		byGUID[it.GUID] = it
	}
	clean, ok := byGUID["guid-clean"]
	if !ok {
		t.Fatal("guid-clean item missing")
	}
	if !clean.Extracted || clean.Failed {
		t.Fatalf("clean item extracted=%v failed=%v, want true/false", clean.Extracted, clean.Failed)
	}
	if !strings.Contains(clean.ContentHTML, "full body text of the clean article") {
		t.Errorf("clean item content missing article text: %q", clean.ContentHTML)
	}
	if strings.Contains(clean.ContentHTML, "Nav cruft text here") {
		t.Errorf("clean item content contains nav text: %q", clean.ContentHTML)
	}
	if strings.Contains(clean.ContentHTML, "footer junk") {
		t.Errorf("clean item content contains footer text: %q", clean.ContentHTML)
	}
	if !clean.FetchedAt.Valid {
		t.Error("clean item fetched_at not set")
	}

	broken, ok := byGUID["guid-broken"]
	if !ok {
		t.Fatal("guid-broken item missing")
	}
	if broken.Extracted || !broken.Failed {
		t.Fatalf("broken item extracted=%v failed=%v, want false/true", broken.Extracted, broken.Failed)
	}
	if broken.ContentHTML != "" {
		t.Errorf("broken item content = %q, want empty", broken.ContentHTML)
	}

	// Second cycle: no article fetches (feed not yet due again for a real
	// tick, and even when due, items are not new anymore).
	before := rs.totalHits()
	s.processOnce(ctx)
	s.waitIdle()
	if after := rs.totalHits(); after != before {
		t.Errorf("second cycle fetched %d more pages, want 0", after-before)
	}

	// Items unchanged after second cycle (no re-extraction).
	clean2, err := st.Item(ctx, clean.ID)
	if err != nil {
		t.Fatalf("reload clean item: %v", err)
	}
	if clean2.ContentHTML != clean.ContentHTML {
		t.Error("clean item content changed after second cycle")
	}
}

func TestFeedSource404MarksError(t *testing.T) {
	ctx := context.Background()
	rs := newRecordingServer(t)
	st := testStore(t)

	feedID, err := st.CreateFeed(ctx, &model.Feed{
		SourceURL:    rs.URL + "/no-such-feed.xml",
		RefreshEvery: time.Hour,
	})
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	s := newTestScheduler(testConfig(), st)
	s.processOnce(ctx)
	s.waitIdle()

	f := feedView(t, st, feedID)
	if f.LastStatus != "error" {
		t.Fatalf("feed last_status = %q, want error", f.LastStatus)
	}
	if f.LastError == "" {
		t.Fatal("feed last_error empty, want an error message")
	}
	if !f.LastRefresh.Valid {
		t.Fatal("feed last_refresh not set")
	}
	items, err := st.ItemsForFeed(ctx, feedID, 10)
	if err != nil {
		t.Fatalf("items: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("got %d items for failed source, want 0", len(items))
	}
}

func TestArticleFetchSpacing(t *testing.T) {
	ctx := context.Background()
	// Both items point at the same host (the httptest server), so the shared
	// Fetcher limiter spaces the article fetches apart.
	rs := newRecordingServer(t)
	st := testStore(t)

	cfg := testConfig()
	cfg.FetchSpacing = 150 * time.Millisecond
	cfg.FetchJitter = 10 * time.Millisecond

	feedID, err := st.CreateFeed(ctx, &model.Feed{
		SourceURL:    rs.URL + "/feed.xml",
		RefreshEvery: time.Hour,
	})
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	s := newTestScheduler(cfg, st)
	s.processOnce(ctx)
	s.waitIdle()

	cleanHits := rs.hitTimes("/clean.html")
	brokenHits := rs.hitTimes("/missing.html")
	if len(cleanHits) != 1 || len(brokenHits) != 1 {
		t.Fatalf("article hits clean=%d broken=%d, want 1 each", len(cleanHits), len(brokenHits))
	}
	// The broken fetch gets a 404 body (which the extractor rejects), so both
	// are "new item" fetches from the same host: the gap must be at least the
	// configured spacing regardless of processing order.
	gap := cleanHits[0].Sub(brokenHits[0])
	if gap < 0 {
		gap = -gap
	}
	if gap < cfg.FetchSpacing {
		t.Errorf("article fetch gap = %v, want >= %v (spacing enforced by shared fetcher)", gap, cfg.FetchSpacing)
	}

	f := feedView(t, st, feedID)
	if f.LastStatus != "ok" {
		t.Fatalf("feed last_status = %q, want ok", f.LastStatus)
	}
}

func TestStartStopLifecycle(t *testing.T) {
	ctx := context.Background()
	rs := newRecordingServer(t)
	st := testStore(t)

	cfg := testConfig()
	s := newTestScheduler(cfg, st)
	s.interval = 30 * time.Millisecond

	feedID, err := st.CreateFeed(ctx, &model.Feed{
		SourceURL:    rs.URL + "/feed.xml",
		RefreshEvery: time.Hour,
	})
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	s.Start(ctx) // also tests idempotence guard: a second Start must be a no-op
	s.Start(ctx)

	// Wait for the loop's initial pass to complete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := feedView(t, st, feedID)
		if f.LastStatus == "ok" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f := feedView(t, st, feedID); f.LastStatus != "ok" {
		t.Fatalf("feed never refreshed by loop, status %q", f.LastStatus)
	}

	s.Stop()
	s.Stop() // idempotent

	// After Stop, in-flight work is done and the loop is gone; a manual
	// processOnce on a fresh context still works.
	s.processOnce(context.Background())
	s.waitIdle()
	if f := feedView(t, st, feedID); f.LastStatus != "ok" {
		t.Fatalf("feed status after stop+manual cycle = %q, want ok", f.LastStatus)
	}
}

func TestNoDuplicateCycleWhileFeedInFlight(t *testing.T) {
	ctx := context.Background()
	rs := newRecordingServer(t)
	st := testStore(t)

	feedID, err := st.CreateFeed(ctx, &model.Feed{
		SourceURL:    rs.URL + "/feed.xml",
		RefreshEvery: 0, // always due
	})
	if err != nil {
		t.Fatalf("create feed: %v", err)
	}

	s := newTestScheduler(testConfig(), st)
	// Cycle 1 claims the feed, cycle 2 fired immediately after must skip it.
	s.processOnce(ctx)
	s.processOnce(ctx)
	s.waitIdle()

	feedHits := rs.hitTimes("/feed.xml")
	if len(feedHits) != 1 {
		t.Errorf("source feed fetched %d times while cycle 1 in flight, want 1", len(feedHits))
	}
	cleanHits := rs.hitTimes("/clean.html")
	if len(cleanHits) != 1 {
		t.Errorf("article fetched %d times, want 1 (single cycle)", len(cleanHits))
	}
	if f := feedView(t, st, feedID); f.LastStatus != "ok" {
		t.Fatalf("feed status = %q, want ok", f.LastStatus)
	}
}

func TestItemFromFeed(t *testing.T) {
	gi := &gofeed.Item{
		Title: "T",
		Link:  "https://x.test/a",
	}
	item, guid := itemFromFeed(gi)
	if guid != "https://x.test/a" {
		t.Errorf("guid = %q, want link fallback", guid)
	}
	if item.GUID != guid {
		t.Errorf("item.GUID = %q, want %q", item.GUID, guid)
	}
}
