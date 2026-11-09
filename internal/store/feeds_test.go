package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"full-text-rss/internal/model"
)

// openTestStore opens a fresh SQLite database in a temp directory.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCreateFeedGeneratesToken(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	f := &model.Feed{SourceURL: "https://example.com/feed.xml", RefreshEvery: 30 * time.Minute}
	id, err := s.CreateFeed(ctx, f)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if id <= 0 {
		t.Fatalf("CreateFeed id = %d, want > 0", id)
	}
	if len(f.Token) != 32 {
		t.Fatalf("token len = %d (%q), want 32 hex chars", len(f.Token), f.Token)
	}
	for _, c := range f.Token {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			t.Fatalf("token %q not lowercase hex", f.Token)
		}
	}
	if f.CreatedAt.IsZero() {
		t.Fatal("CreatedAt not set")
	}
}

func TestCreateFeedPreservesGivenToken(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := &model.Feed{Token: "customtoken", SourceURL: "https://example.com/rss"}
	if _, err := s.CreateFeed(ctx, f); err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if f.Token != "customtoken" {
		t.Fatalf("token = %q, want preserved", f.Token)
	}
}

func TestFeedRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	lastRefresh := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	f := &model.Feed{
		SourceURL:    "https://example.org/atom.xml",
		Title:        "Example",
		RefreshEvery: 90 * time.Minute,
	}
	id, err := s.CreateFeed(ctx, f)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if err := s.MarkFeedRefreshed(ctx, id, "ok", "", lastRefresh); err != nil {
		t.Fatalf("MarkFeedRefreshed: %v", err)
	}

	got, err := s.Feed(ctx, id)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if got.Token != f.Token || got.SourceURL != f.SourceURL || got.Title != "Example" {
		t.Fatalf("Feed mismatch: %+v", got)
	}
	if got.RefreshEvery != 90*time.Minute {
		t.Fatalf("RefreshEvery = %v, want 90m", got.RefreshEvery)
	}
	if !got.LastRefresh.Valid || !got.LastRefresh.Time.Equal(lastRefresh) {
		t.Fatalf("LastRefresh = %+v, want %v", got.LastRefresh, lastRefresh)
	}
	if got.LastStatus != "ok" {
		t.Fatalf("LastStatus = %q, want ok", got.LastStatus)
	}

	byToken, err := s.FeedByToken(ctx, f.Token)
	if err != nil {
		t.Fatalf("FeedByToken: %v", err)
	}
	if byToken.ID != id {
		t.Fatalf("FeedByToken id = %d, want %d", byToken.ID, id)
	}

	feeds, err := s.Feeds(ctx)
	if err != nil {
		t.Fatalf("Feeds: %v", err)
	}
	if len(feeds) != 1 || feeds[0].ID != id {
		t.Fatalf("Feeds = %+v, want one feed id %d", feeds, id)
	}
}

func TestFeedMissingReturnsNoRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.Feed(ctx, 12345); err != sql.ErrNoRows {
		t.Fatalf("Feed missing: err = %v, want sql.ErrNoRows", err)
	}
	if _, err := s.FeedByToken(ctx, "nope"); err != sql.ErrNoRows {
		t.Fatalf("FeedByToken missing: err = %v, want sql.ErrNoRows", err)
	}
}

func TestUpdateFeed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	f := &model.Feed{SourceURL: "https://a.com/feed", RefreshEvery: time.Hour}
	id, err := s.CreateFeed(ctx, f)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	f.ID = id
	f.SourceURL = "https://b.com/feed"
	f.Title = "Renamed"
	f.RefreshEvery = 2 * time.Hour
	if err := s.UpdateFeed(ctx, f); err != nil {
		t.Fatalf("UpdateFeed: %v", err)
	}
	got, err := s.Feed(ctx, id)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if got.SourceURL != "https://b.com/feed" || got.Title != "Renamed" || got.RefreshEvery != 2*time.Hour {
		t.Fatalf("after UpdateFeed: %+v", got)
	}
	// Update of a missing feed must report sql.ErrNoRows.
	if err := s.UpdateFeed(ctx, &model.Feed{ID: 999}); err != sql.ErrNoRows {
		t.Fatalf("UpdateFeed missing: err = %v, want sql.ErrNoRows", err)
	}
}

func TestDeleteFeedCascadesItems(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fid, err := s.CreateFeed(ctx, &model.Feed{SourceURL: "https://c.com/feed"})
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if _, err := s.UpsertItem(ctx, fid, &model.Item{GUID: "g1", Title: "one"}); err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if err := s.DeleteFeed(ctx, fid); err != nil {
		t.Fatalf("DeleteFeed: %v", err)
	}
	if _, err := s.Feed(ctx, fid); err != sql.ErrNoRows {
		t.Fatalf("Feed after delete: err = %v, want sql.ErrNoRows", err)
	}
	items, err := s.ItemsForFeed(ctx, fid, 10)
	if err != nil {
		t.Fatalf("ItemsForFeed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items not cascaded: %+v", items)
	}
	if err := s.DeleteFeed(ctx, fid); err != sql.ErrNoRows {
		t.Fatalf("DeleteFeed missing: err = %v, want sql.ErrNoRows", err)
	}
}

func TestDueFeeds(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

	// never refreshed -> due
	neverID, err := s.CreateFeed(ctx, &model.Feed{SourceURL: "https://never.com/f", RefreshEvery: time.Hour})
	if err != nil {
		t.Fatalf("CreateFeed never: %v", err)
	}
	// refreshed long ago -> due
	oldID, err := s.CreateFeed(ctx, &model.Feed{SourceURL: "https://old.com/f", RefreshEvery: time.Hour})
	if err != nil {
		t.Fatalf("CreateFeed old: %v", err)
	}
	// refreshed just now, interval 1h -> not due
	freshID, err := s.CreateFeed(ctx, &model.Feed{SourceURL: "https://fresh.com/f", RefreshEvery: time.Hour})
	if err != nil {
		t.Fatalf("CreateFeed fresh: %v", err)
	}
	if err := s.MarkFeedRefreshed(ctx, oldID, "ok", "", base.Add(-2*time.Hour)); err != nil {
		t.Fatalf("MarkFeedRefreshed old: %v", err)
	}
	if err := s.MarkFeedRefreshed(ctx, freshID, "ok", "", base.Add(-30*time.Minute)); err != nil {
		t.Fatalf("MarkFeedRefreshed fresh: %v", err)
	}

	due, err := s.DueFeeds(ctx, base)
	if err != nil {
		t.Fatalf("DueFeeds: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("DueFeeds len = %d (%+v), want 2", len(due), due)
	}
	if due[0].ID != neverID || due[1].ID != oldID {
		t.Fatalf("DueFeeds order = [%d,%d], want [%d,%d] (never first, then most overdue)",
			due[0].ID, due[1].ID, neverID, oldID)
	}

	// Boundary: exactly at last_refresh + interval, the feed is due.
	boundary := base // fresh feed was refreshed at base-30m with 1h interval; use its exact due time
	fresh, err := s.Feed(ctx, freshID)
	if err != nil {
		t.Fatalf("Feed fresh: %v", err)
	}
	boundary = fresh.LastRefresh.Time.Add(time.Hour)
	due, err = s.DueFeeds(ctx, boundary)
	if err != nil {
		t.Fatalf("DueFeeds boundary: %v", err)
	}
	found := false
	for _, f := range due {
		switch f.ID {
		case freshID:
			found = true
		case neverID, oldID:
			// never refreshed: always due; old refreshed 2h ago with a 1h
			// interval: also due at boundary.
		default:
			t.Fatalf("unexpected feed %d due", f.ID)
		}
	}
	if !found {
		t.Fatalf("fresh feed not due at exact boundary %v; due = %+v", boundary, due)
	}
	// one second before the boundary it must not be due
	due, err = s.DueFeeds(ctx, boundary.Add(-time.Second))
	if err != nil {
		t.Fatalf("DueFeeds pre-boundary: %v", err)
	}
	for _, f := range due {
		if f.ID == freshID {
			t.Fatalf("fresh feed due 1s early at %v", boundary.Add(-time.Second))
		}
	}
}

func TestMarkFeedRefreshedError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fid, err := s.CreateFeed(ctx, &model.Feed{SourceURL: "https://e.com/feed"})
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	msg := "boom: 500"
	now := time.Now().UTC()
	if err := s.MarkFeedRefreshed(ctx, fid, "error", msg, now); err != nil {
		t.Fatalf("MarkFeedRefreshed: %v", err)
	}
	f, err := s.Feed(ctx, fid)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if f.LastStatus != "error" || f.LastError != msg {
		t.Fatalf("status/err = %q/%q, want error/%q", f.LastStatus, f.LastError, msg)
	}
	if !f.LastRefresh.Valid || !f.LastRefresh.Time.Equal(now) {
		t.Fatalf("LastRefresh = %+v, want %v (UTC)", f.LastRefresh, now)
	}
	if want := "UTC"; f.LastRefresh.Time.Location().String() != want {
		t.Fatalf("LastRefresh location = %v, want %v", f.LastRefresh.Time.Location(), want)
	}
}

func TestDueFeedsEmpty(t *testing.T) {
	s := openTestStore(t)
	due, err := s.DueFeeds(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("DueFeeds: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("DueFeeds = %+v, want empty", due)
	}
}
