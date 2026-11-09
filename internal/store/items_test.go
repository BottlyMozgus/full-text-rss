package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"rss-scraper/internal/model"
)

func mkFeed(t *testing.T, s *Store) int64 {
	t.Helper()
	id, err := s.CreateFeed(context.Background(), &model.Feed{SourceURL: "https://items.test/feed"})
	if err != nil {
		t.Fatalf("CreateFeed: %v", err)
	}
	return id
}

func TestUpsertItemDedupe(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fid := mkFeed(t, s)

	pub := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	it := &model.Item{
		GUID:        "guid-1",
		URL:         "https://items.test/a",
		Title:       "First",
		Author:      "Ada",
		PublishedAt: sql.NullTime{Time: pub, Valid: true},
		Categories:  []string{"go", "rss"},
		Summary:     "summary text",
	}
	isNew, err := s.UpsertItem(ctx, fid, it)
	if err != nil {
		t.Fatalf("UpsertItem: %v", err)
	}
	if !isNew {
		t.Fatal("first insert isNew = false, want true")
	}
	if it.ID <= 0 {
		t.Fatalf("it.ID not set: %d", it.ID)
	}
	firstID := it.ID

	// Same (feed_id, guid) -> update, isNew=false.
	it2 := &model.Item{
		GUID:    "guid-1",
		URL:     "https://items.test/a-updated",
		Title:   "First (edited)",
		Summary: "new summary",
	}
	isNew, err = s.UpsertItem(ctx, fid, it2)
	if err != nil {
		t.Fatalf("UpsertItem 2: %v", err)
	}
	if isNew {
		t.Fatal("second upsert isNew = true, want false")
	}
	if it2.ID != firstID {
		t.Fatalf("second upsert id = %d, want existing %d", it2.ID, firstID)
	}

	// Same guid on a different feed is a separate row.
	fid2 := mkFeed(t, s)
	isNew, err = s.UpsertItem(ctx, fid2, &model.Item{GUID: "guid-1"})
	if err != nil {
		t.Fatalf("UpsertItem other feed: %v", err)
	}
	if !isNew {
		t.Fatal("insert on other feed isNew = false, want true")
	}

	got, err := s.Item(ctx, firstID)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if got.Title != "First (edited)" || got.URL != "https://items.test/a-updated" || got.Summary != "new summary" {
		t.Fatalf("after update: %+v", got)
	}
	// Sync metadata is overwritten from the latest parse...
	if got.Author != "" {
		t.Fatalf("Author = %q, want overwritten from latest parse", got.Author)
	}
	if len(got.Categories) != 0 {
		t.Fatalf("Categories = %v, want overwritten to empty", got.Categories)
	}
	if got.PublishedAt.Valid {
		t.Fatalf("PublishedAt = %+v, want overwritten to null", got.PublishedAt)
	}
	// ...while extraction state is untouched.
	if got.Extracted || got.Failed || got.FetchedAt.Valid {
		t.Fatalf("extraction state changed: %+v", got)
	}
}

func TestItemsForFeedOrderAndLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fid := mkFeed(t, s)

	// Insert out of order, with mixed published_at (some null).
	items := []*model.Item{
		{GUID: "g1", Title: "old", PublishedAt: sql.NullTime{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}},
		{GUID: "g2", Title: "newest", PublishedAt: sql.NullTime{Time: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Valid: true}},
		{GUID: "g3", Title: "middle", PublishedAt: sql.NullTime{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Valid: true}},
		{GUID: "g4", Title: "no-date"},
	}
	for _, it := range items {
		if _, err := s.UpsertItem(ctx, fid, it); err != nil {
			t.Fatalf("UpsertItem %s: %v", it.GUID, err)
		}
	}

	got, err := s.ItemsForFeed(ctx, fid, 10)
	if err != nil {
		t.Fatalf("ItemsForFeed: %v", err)
	}
	wantOrder := []string{"newest", "middle", "old", "no-date"}
	if len(got) != len(wantOrder) {
		t.Fatalf("len = %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].Title != want {
			t.Fatalf("pos %d = %q, want %q", i, got[i].Title, want)
		}
	}
	if got[0].FeedID != fid {
		t.Fatalf("FeedID = %d, want %d", got[0].FeedID, fid)
	}

	// Limit returns only the newest N.
	got, err = s.ItemsForFeed(ctx, fid, 2)
	if err != nil {
		t.Fatalf("ItemsForFeed limit: %v", err)
	}
	if len(got) != 2 || got[0].Title != "newest" || got[1].Title != "middle" {
		t.Fatalf("limited = %+v, want newest,middle", got)
	}
}

func TestPendingItemsAndUpdateItemContent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	fid := mkFeed(t, s)

	ids := []int64{}
	for _, g := range []string{"p1", "p2", "p3"} {
		it := &model.Item{GUID: g}
		if _, err := s.UpsertItem(ctx, fid, it); err != nil {
			t.Fatalf("UpsertItem %s: %v", g, err)
		}
		ids = append(ids, it.ID)
	}

	pending, err := s.PendingItems(ctx, fid)
	if err != nil {
		t.Fatalf("PendingItems: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("PendingItems len = %d, want 3", len(pending))
	}
	for i, it := range pending {
		if it.Extracted {
			t.Fatalf("pending item %d marked extracted", it.ID)
		}
		if it.ID != ids[i] {
			t.Fatalf("pending pos %d id = %d, want %d (insertion order)", i, it.ID, ids[i])
		}
	}

	// Extract one, fail one. Per the contract PendingItems selects
	// extracted=false literally, so the failed item remains pending and is
	// retried on the next cycle.
	if err := s.UpdateItemContent(ctx, ids[0], "<p>full text</p>", true, false); err != nil {
		t.Fatalf("UpdateItemContent: %v", err)
	}
	if err := s.UpdateItemContent(ctx, ids[1], "", false, true); err != nil {
		t.Fatalf("UpdateItemContent fail: %v", err)
	}

	pending, err = s.PendingItems(ctx, fid)
	if err != nil {
		t.Fatalf("PendingItems after: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("PendingItems after len = %d, want 2 (failed items stay pending)", len(pending))
	}
	if pending[0].ID != ids[1] || pending[1].ID != ids[2] {
		t.Fatalf("PendingItems after = [%d,%d], want [%d,%d]",
			pending[0].ID, pending[1].ID, ids[1], ids[2])
	}
	if !pending[0].Failed {
		t.Fatalf("pending failed item lost Failed flag: %+v", pending[0])
	}

	ok, err := s.Item(ctx, ids[0])
	if err != nil {
		t.Fatalf("Item ok: %v", err)
	}
	if ok.ContentHTML != "<p>full text</p>" || !ok.Extracted || ok.Failed {
		t.Fatalf("extracted item: %+v", ok)
	}
	if !ok.FetchedAt.Valid || ok.FetchedAt.Time.IsZero() {
		t.Fatalf("FetchedAt not set: %+v", ok.FetchedAt)
	}

	fail, err := s.Item(ctx, ids[1])
	if err != nil {
		t.Fatalf("Item fail: %v", err)
	}
	if fail.ContentHTML != "" || fail.Extracted || !fail.Failed {
		t.Fatalf("failed item: %+v", fail)
	}

	if err := s.UpdateItemContent(ctx, 424242, "<p>x</p>", true, false); err != sql.ErrNoRows {
		t.Fatalf("UpdateItemContent missing: err = %v, want sql.ErrNoRows", err)
	}
}

func TestPendingItemsEmpty(t *testing.T) {
	s := openTestStore(t)
	pending, err := s.PendingItems(context.Background(), 999)
	if err != nil {
		t.Fatalf("PendingItems: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("PendingItems = %+v, want empty", pending)
	}
}

func TestItemMissing(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Item(context.Background(), 31337); err != sql.ErrNoRows {
		t.Fatalf("Item missing: err = %v, want sql.ErrNoRows", err)
	}
}
