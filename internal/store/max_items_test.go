package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"rss-scraper/internal/model"
)

func TestFeedMaxItemsDefaultsAndRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Unset on create -> default, also written back to the struct.
	def := &model.Feed{SourceURL: "https://a.com/feed", RefreshEvery: time.Hour}
	defID, err := s.CreateFeed(ctx, def)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if def.MaxItems != model.DefaultMaxItems {
		t.Errorf("struct MaxItems = %d, want %d", def.MaxItems, model.DefaultMaxItems)
	}
	got, err := s.Feed(ctx, defID)
	if err != nil {
		t.Fatalf("Feed: %v", err)
	}
	if got.MaxItems != 20 {
		t.Errorf("stored default MaxItems = %d, want 20", got.MaxItems)
	}

	// Explicit value survives create, update, and every read path.
	f := &model.Feed{SourceURL: "https://b.com/feed", RefreshEvery: time.Hour, MaxItems: 5}
	id, err := s.CreateFeed(ctx, f)
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	if got, _ = s.Feed(ctx, id); got.MaxItems != 5 {
		t.Errorf("created MaxItems = %d, want 5", got.MaxItems)
	}
	got.MaxItems = 50
	if err := s.UpdateFeed(ctx, got); err != nil {
		t.Fatalf("UpdateFeed: %v", err)
	}
	byToken, err := s.FeedByToken(ctx, f.Token)
	if err != nil {
		t.Fatalf("FeedByToken: %v", err)
	}
	if byToken.MaxItems != 50 {
		t.Errorf("updated MaxItems = %d, want 50", byToken.MaxItems)
	}
	feeds, err := s.Feeds(ctx)
	if err != nil {
		t.Fatalf("Feeds: %v", err)
	}
	if len(feeds) != 2 || feeds[0].MaxItems != 20 || feeds[1].MaxItems != 50 {
		t.Errorf("Feeds MaxItems = %+v, want 20 and 50", feeds)
	}

	// An unset value on update falls back to the default, never to 0.
	byToken.MaxItems = 0
	if err := s.UpdateFeed(ctx, byToken); err != nil {
		t.Fatalf("UpdateFeed zero: %v", err)
	}
	if got, _ = s.Feed(ctx, id); got.MaxItems != model.DefaultMaxItems {
		t.Errorf("MaxItems after zero update = %d, want %d", got.MaxItems, model.DefaultMaxItems)
	}
}

// A database written before migrations were versioned (schema 001 only,
// user_version 0) must gain max_items without losing rows.
func TestOpenUpgradesUnversionedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	init001, err := migrationsFS.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read 001: %v", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec(string(init001)); err != nil {
		t.Fatalf("apply 001: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO feeds (token, source_url, title, refresh_every_seconds)
		VALUES ('legacytoken', 'https://old.example/feed', 'Old feed', 1800)`); err != nil {
		t.Fatalf("seed feed: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO items (feed_id, guid, title) VALUES (1, 'g-old', 'Old item')`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	ctx := context.Background()
	// Open twice: the second pass must not re-run the ALTER TABLE.
	for pass := 1; pass <= 2; pass++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open pass %d: %v", pass, err)
		}
		f, err := s.FeedByToken(ctx, "legacytoken")
		if err != nil {
			t.Fatalf("pass %d FeedByToken: %v", pass, err)
		}
		if f.Title != "Old feed" || f.RefreshEvery != 30*time.Minute {
			t.Errorf("pass %d feed changed: %+v", pass, f)
		}
		if f.MaxItems != model.DefaultMaxItems {
			t.Errorf("pass %d MaxItems = %d, want %d", pass, f.MaxItems, model.DefaultMaxItems)
		}
		items, err := s.ItemsForFeed(ctx, f.ID, 10)
		if err != nil || len(items) != 1 || items[0].GUID != "g-old" {
			t.Errorf("pass %d items = %+v, err %v", pass, items, err)
		}
		var version int
		if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatalf("pass %d user_version: %v", pass, err)
		}
		if version != 2 {
			t.Errorf("pass %d user_version = %d, want 2", pass, version)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("pass %d Close: %v", pass, err)
		}
	}
}
