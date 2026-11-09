package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rss-scraper/internal/model"
)

// encodeCategories joins categories comma-separated for storage. A category
// containing a comma is stored literally; decode splits on commas.
func encodeCategories(cats []string) string {
	return strings.Join(cats, ",")
}

// decodeCategories splits the stored comma-separated categories.
func decodeCategories(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	return parts
}

// UpsertItem inserts it for feedID, or updates the existing row when
// (feed_id, guid) already exists. isNew is true only when a new row was
// inserted; on update it is false. Sets it.ID on insert, or to the existing
// row's id on update.
//
// Only sync metadata (url, title, author, published_at, categories, summary)
// is overwritten; extraction results are left untouched. Select-then-write
// rather than ON CONFLICT because modernc's RowsAffected does not
// distinguish insert from conflict-update; MaxOpenConns(1) makes the
// read-then-write safe within one Store.
func (s *Store) UpsertItem(ctx context.Context, feedID int64, it *model.Item) (bool, error) {
	var existing int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM items WHERE feed_id = ? AND guid = ?`, feedID, it.GUID).
		Scan(&existing)
	switch {
	case err == sql.ErrNoRows:
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO items (feed_id, guid, url, title, author, published_at, categories, summary, content_html, extracted, failed, fetched_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			feedID, it.GUID, it.URL, it.Title, it.Author,
			nullTime(it.PublishedAt), encodeCategories(it.Categories),
			it.Summary, it.ContentHTML, boolInt(it.Extracted), boolInt(it.Failed),
			nullTime(it.FetchedAt))
		if err != nil {
			return false, fmt.Errorf("insert item feed %d guid %q: %w", feedID, it.GUID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return false, fmt.Errorf("insert item feed %d guid %q: %w", feedID, it.GUID, err)
		}
		it.ID = id
		return true, nil
	case err != nil:
		return false, fmt.Errorf("upsert item feed %d guid %q: %w", feedID, it.GUID, err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE items SET url = ?, title = ?, author = ?, published_at = ?, categories = ?, summary = ?
		 WHERE id = ?`,
		it.URL, it.Title, it.Author, nullTime(it.PublishedAt),
		encodeCategories(it.Categories), it.Summary, existing)
	if err != nil {
		return false, fmt.Errorf("update item feed %d guid %q: %w", feedID, it.GUID, err)
	}
	it.ID = existing
	return false, nil
}

// ItemsForFeed returns up to limit items for the feed, newest first
// (published_at desc when set, else id desc; items without a publish time
// come last).
func (s *Store) ItemsForFeed(ctx context.Context, feedID int64, limit int) ([]model.Item, error) {
	rows, err := s.db.QueryContext(ctx, itemCols+`
		WHERE feed_id = ?
		ORDER BY (published_at IS NULL) ASC, published_at DESC, id DESC
		LIMIT ?`, feedID, limit)
	if err != nil {
		return nil, fmt.Errorf("items for feed %d: %w", feedID, err)
	}
	return collectItems(rows)
}

// PendingItems returns items of the feed that have not been extracted yet
// (extracted=false), oldest first, so the pipeline processes them in feed
// order.
func (s *Store) PendingItems(ctx context.Context, feedID int64) ([]model.Item, error) {
	rows, err := s.db.QueryContext(ctx, itemCols+`
		WHERE feed_id = ? AND extracted = 0
		ORDER BY id ASC`, feedID)
	if err != nil {
		return nil, fmt.Errorf("pending items for feed %d: %w", feedID, err)
	}
	return collectItems(rows)
}

// Item returns the item with the given id, sql.ErrNoRows if missing.
func (s *Store) Item(ctx context.Context, id int64) (*model.Item, error) {
	row := s.db.QueryRowContext(ctx, itemCols+` WHERE id = ?`, id)
	it, err := scanItem(row.Scan)
	if err != nil {
		return nil, err
	}
	return it, nil
}

// UpdateItemContent stores the extracted HTML and flags on the item.
func (s *Store) UpdateItemContent(ctx context.Context, id int64, html string, extracted, failed bool) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE items SET content_html = ?, extracted = ?, failed = ?, fetched_at = ? WHERE id = ?`,
		html, boolInt(extracted), boolInt(failed), now, id)
	if err != nil {
		return fmt.Errorf("update item %d content: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update item %d content: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const itemCols = `SELECT id, feed_id, guid, url, title, author, published_at,
	categories, summary, content_html, extracted, failed, fetched_at FROM items`

// boolInt converts a bool to its SQLite integer form.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullTime converts sql.NullTime to a driver value (nil when invalid).
func nullTime(nt sql.NullTime) any {
	if nt.Valid {
		return nt.Time
	}
	return nil
}

// scanItem adapts a row scan into a model.Item.
func scanItem(scan func(dest ...any) error) (*model.Item, error) {
	var (
		it        model.Item
		cats      string
		extracted int
		failed    int
		published sql.NullTime
		fetched   sql.NullTime
	)
	if err := scan(&it.ID, &it.FeedID, &it.GUID, &it.URL, &it.Title, &it.Author,
		&published, &cats, &it.Summary, &it.ContentHTML, &extracted, &failed,
		&fetched); err != nil {
		return nil, err
	}
	it.Categories = decodeCategories(cats)
	it.Extracted = extracted != 0
	it.Failed = failed != 0
	it.PublishedAt = published
	it.FetchedAt = fetched
	return &it, nil
}

// collectItems drains rows into a slice.
func collectItems(rows *sql.Rows) ([]model.Item, error) {
	defer rows.Close()
	var out []model.Item
	for rows.Next() {
		it, err := scanItem(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}
