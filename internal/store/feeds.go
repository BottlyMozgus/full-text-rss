package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"rss-scraper/internal/model"
)

// generateToken returns 32 hex chars from crypto/rand.
func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateFeed inserts f, generating a token if f.Token is empty, and sets
// f.ID and f.CreatedAt. Zero RefreshEvery defaults to 3600s in the schema;
// MaxItems below 1 becomes model.DefaultMaxItems.
func (s *Store) CreateFeed(ctx context.Context, f *model.Feed) (int64, error) {
	if f.Token == "" {
		tok, err := generateToken()
		if err != nil {
			return 0, err
		}
		f.Token = tok
	}
	f.MaxItems = maxItemsOrDefault(f.MaxItems)
	created := f.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO feeds (token, source_url, title, refresh_every_seconds, max_items, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		f.Token, f.SourceURL, f.Title, int64(f.RefreshEvery.Seconds()), f.MaxItems, created)
	if err != nil {
		return 0, fmt.Errorf("create feed: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create feed: %w", err)
	}
	f.ID = id
	f.CreatedAt = created
	return id, nil
}

// UpdateFeed persists all editable fields of f. It does not touch the token,
// scheduler bookkeeping, or created_at. MaxItems below 1 becomes
// model.DefaultMaxItems.
func (s *Store) UpdateFeed(ctx context.Context, f *model.Feed) error {
	f.MaxItems = maxItemsOrDefault(f.MaxItems)
	res, err := s.db.ExecContext(ctx,
		`UPDATE feeds SET source_url = ?, title = ?, refresh_every_seconds = ?, max_items = ? WHERE id = ?`,
		f.SourceURL, f.Title, int64(f.RefreshEvery.Seconds()), f.MaxItems, f.ID)
	if err != nil {
		return fmt.Errorf("update feed %d: %w", f.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update feed %d: %w", f.ID, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteFeed removes the feed and cascades its items.
func (s *Store) DeleteFeed(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM feeds WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete feed %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete feed %d: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Feed returns the feed with the given id, sql.ErrNoRows if missing.
func (s *Store) Feed(ctx context.Context, id int64) (*model.Feed, error) {
	row := s.db.QueryRowContext(ctx, feedCols+` WHERE id = ?`, id)
	return scanFeed(row.Scan)
}

// FeedByToken returns the feed with the given token, sql.ErrNoRows if missing.
func (s *Store) FeedByToken(ctx context.Context, token string) (*model.Feed, error) {
	row := s.db.QueryRowContext(ctx, feedCols+` WHERE token = ?`, token)
	return scanFeed(row.Scan)
}

// Feeds returns all feeds ordered by creation time.
func (s *Store) Feeds(ctx context.Context) ([]model.Feed, error) {
	rows, err := s.db.QueryContext(ctx, feedCols+` ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list feeds: %w", err)
	}
	defer rows.Close()
	var out []model.Feed
	for rows.Next() {
		f, err := scanFeed(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("list feeds: %w", err)
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list feeds: %w", err)
	}
	return out, nil
}

// DueFeeds returns feeds whose last_refresh is NULL or older than their
// refresh interval as of now, never-refreshed first, then most overdue.
// The comparison runs in Go because the driver's stored time format is not
// parseable by SQLite's date functions.
func (s *Store) DueFeeds(ctx context.Context, now time.Time) ([]model.Feed, error) {
	feeds, err := s.Feeds(ctx)
	if err != nil {
		return nil, fmt.Errorf("due feeds: %w", err)
	}
	var out []model.Feed
	for _, f := range feeds {
		if f.LastRefresh.Valid && now.Sub(f.LastRefresh.Time) < f.RefreshEvery {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		vi, vj := out[i].LastRefresh.Valid, out[j].LastRefresh.Valid
		if vi != vj {
			return !vi // never refreshed first
		}
		if !vi {
			return false
		}
		return out[i].LastRefresh.Time.Before(out[j].LastRefresh.Time)
	})
	return out, nil
}

// MarkFeedRefreshed sets last_refresh, last_status and last_error on the feed.
func (s *Store) MarkFeedRefreshed(ctx context.Context, id int64, status, errMsg string, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE feeds SET last_refresh = ?, last_status = ?, last_error = ? WHERE id = ?`,
		now, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("mark feed %d refreshed: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark feed %d refreshed: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const feedCols = `SELECT id, token, source_url, title, refresh_every_seconds,
	max_items, last_refresh, last_status, last_error, created_at FROM feeds`

// maxItemsOrDefault maps an unset (non-positive) limit to the default.
func maxItemsOrDefault(n int) int {
	if n < 1 {
		return model.DefaultMaxItems
	}
	return n
}

// scanFeed adapts a row scan into a model.Feed.
func scanFeed(scan func(dest ...any) error) (*model.Feed, error) {
	var (
		f          model.Feed
		refreshSec int64
		lastRef    sql.NullTime
	)
	if err := scan(&f.ID, &f.Token, &f.SourceURL, &f.Title, &refreshSec,
		&f.MaxItems, &lastRef, &f.LastStatus, &f.LastError, &f.CreatedAt); err != nil {
		return nil, err
	}
	f.RefreshEvery = time.Duration(refreshSec) * time.Second
	f.LastRefresh = lastRef
	return &f, nil
}
