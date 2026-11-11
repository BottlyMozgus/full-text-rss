package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mmcdole/gofeed"
	"rss-scraper/internal/model"
)

// Regression test for the feed reported at https://rss.orf.at/news.xml
// (RSS 1.0, dc:date with numeric UTC offsets): gofeed parses such dates with
// time.Parse, producing a zone with an EMPTY name on hosts whose local zone
// does not resolve the offset to a named zone. The default sqlite driver
// serialization then stored strings its own reader cannot parse, failing
// every later scan into time.Time. Run with TZ=UTC to exercise the broken
// shape; with TZ=Europe/Vienna the old code happened to round-trip.
func TestNumericOffsetDatesRoundTrip(t *testing.T) {
	if os.Getenv("TZ") != "UTC" {
		t.Skip("run with TZ=UTC to reproduce the empty-zone-name parse")
	}
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
  xmlns:dc="http://purl.org/dc/elements/1.1/"
  xmlns="http://purl.org/rss/1.0/">
<channel rdf:about="https://orf.at/"><title>news.ORF.at</title><link>https://orf.at/</link></channel>
<item rdf:about="https://orf.at/stories/1/">
  <title>Story one</title><link>https://orf.at/stories/1/</link>
  <dc:date>2026-11-11T18:32:16+01:00</dc:date>
</item>
<item rdf:about="https://orf.at/stories/2/">
  <title>Story two</title><link>https://orf.at/stories/2/</link>
  <dc:date>2026-11-11T17:00:00+05:30</dc:date>
</item>
</rdf:RDF>`
	feed, err := gofeed.NewParser().ParseString(xml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	s := openTestStore(t)
	ctx := context.Background()
	fid := mkFeed(t, s)
	for _, gi := range feed.Items {
		it := &model.Item{GUID: gi.Link, URL: gi.Link, Title: gi.Title}
		if gi.PublishedParsed != nil {
			it.PublishedAt = sql.NullTime{Time: *gi.PublishedParsed, Valid: true}
		}
		if _, err := s.UpsertItem(ctx, fid, it); err != nil {
			t.Fatalf("UpsertItem: %v", err)
		}
	}

	items, err := s.ItemsForFeed(ctx, fid, 10)
	if err != nil {
		t.Fatalf("ItemsForFeed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
	// Newest first: 17:30 UTC beats 16:00 UTC.
	if items[0].Title != "Story one" || items[1].Title != "Story two" {
		t.Fatalf("order = [%s, %s], want newest first", items[0].Title, items[1].Title)
	}
	want := time.Date(2026, 11, 11, 17, 32, 16, 0, time.UTC)
	if got := items[0].PublishedAt.Time; !got.Equal(want) {
		t.Fatalf("published = %v, want %v (instant preserved)", got, want)
	}
}

// nullTime must store UTC so the driver's serialization is always readable,
// whatever zone name the parser produced.
func TestNullTimeNormalizesToUTC(t *testing.T) {
	emptyName := time.FixedZone("", 3600)
	v := nullTime(sql.NullTime{Time: time.Date(2026, 11, 11, 18, 32, 16, 0, emptyName), Valid: true})
	tm, ok := v.(time.Time)
	if !ok {
		t.Fatalf("nullTime = %T, want time.Time", v)
	}
	if tm.Location() != time.UTC {
		t.Fatalf("zone = %v, want UTC", tm.Location())
	}
	if !tm.Equal(time.Date(2026, 11, 11, 17, 32, 16, 0, time.UTC)) {
		t.Fatalf("instant changed: %v", tm)
	}
	if nullTime(sql.NullTime{}) != nil {
		t.Fatal("invalid NullTime must bind nil")
	}
}

// A store written by an older binary (default String()-based time
// serialization, no _time_format) can hold unparseable values; opening with
// the current code must run migration 004 and make the rows readable again.
func TestMigration004RepairsPoisonedTimes(t *testing.T) {
	if os.Getenv("TZ") != "UTC" {
		t.Skip("run with TZ=UTC so seeded upserts write the poisoned shape")
	}
	ctx := context.Background()
	dir := t.TempDir()

	// Open with the default serialization to create schema v3 (migration 004
	// not yet applied), seed a poisoned row, and close. Open's DSN now sets
	// _time_format=sqlite, so drive the database directly instead.
	dbPath := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	emptyName := time.FixedZone("", 3600)
	pub := time.Date(2026, 11, 11, 18, 32, 16, 0, emptyName)
	if _, err := db.Exec(
		`INSERT INTO items (feed_id, guid, published_at) VALUES (1, 'poisoned', ?)`, pub); err == nil {
		t.Fatal("expected schema-missing failure, got none")
	}
	db.Close()

	// Build the schema through the current code path but with migrations
	// limited to v3: simplest is to run the current Open on a fresh path,
	// copy its schema, then downgrade user_version and rewrite the value in
	// the poisoned shape. Instead, exercise the real flow: current Open on
	// the seeded file must both create and repair.
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	// Schema now exists (v4). Seed a feed and the poisoned value with the
	// legacy writer semantics by string, exactly as the old driver stored it.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO feeds (id, token, source_url, title) VALUES (1, 'tok', 'https://x/feed', 'X')`); err != nil {
		t.Fatalf("seed feed: %v", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO items (feed_id, guid, published_at) VALUES (1, 'poisoned', ?)`,
		"2026-11-11 18:32:16 +0100 +0100"); err != nil {
		t.Fatalf("seed poisoned: %v", err)
	}

	// Migration 004 already ran at Open, before the seed: force it again by
	// rewinding user_version and reopening.
	if _, err := s.db.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		t.Fatalf("rewind user_version: %v", err)
	}
	s.Close()
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	items, err := s2.ItemsForFeed(ctx, 1, 10)
	if err != nil {
		t.Fatalf("ItemsForFeed after repair: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	want := time.Date(2026, 11, 11, 17, 32, 16, 0, time.UTC)
	if !items[0].PublishedAt.Valid || !items[0].PublishedAt.Time.Equal(want) {
		t.Fatalf("repaired published_at = %+v, want %v", items[0].PublishedAt, want)
	}
}

// Migration 004 GLOB surgery: poisoned strings are rewritten, valid ones are
// untouched. Exercises the SQL statements directly against a scratch DB.
func TestMigration004GLOBSurgery(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	defer s.Close()
	stmt := `UPDATE items SET published_at =
    substr(published_at, 1, length(published_at) - 12)
    || substr(published_at, length(published_at) - 4, 3)
    || ':'
    || substr(published_at, length(published_at) - 1, 2)
WHERE published_at GLOB '*[0-9] [+-][0-9][0-9][0-9][0-9] [+-][0-9][0-9][0-9][0-9]'`

	fid := mkFeed(t, s)
	cases := []struct {
		name string
		in   string
		want string // empty means "must stay unchanged"
	}{
		{"poisoned unnamed zone", "2026-11-11 18:32:16 +0100 +0100", "2026-11-11 18:32:16+01:00"},
		{"iso (new format)", "2026-11-11 18:32:16+01:00", ""},
		{"named zone", "2026-11-11 18:32:16 +0100 CET", ""},
		{"fractional named zone", "2026-11-11 18:32:16.123 +0100 CET", ""},
		{"monotonic suffix", "2026-11-11 18:32:16.5 +0000 UTC m=+0.1", ""},
	}
	for _, tc := range cases {
		guid := "mig-" + tc.name
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO items (feed_id, guid, published_at) VALUES (?, ?, ?)`,
			fid, guid, tc.in); err != nil {
			t.Fatalf("seed %s: %v", tc.name, err)
		}
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("update: %v", err)
		}
		var got string
		if err := s.db.QueryRowContext(ctx,
			`SELECT CAST(published_at AS TEXT) FROM items WHERE guid = ?`, guid).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		want := tc.want
		if want == "" {
			want = tc.in
		}
		if got != want {
			t.Fatalf("%s: got %q, want %q", tc.name, got, want)
		}
	}
}
