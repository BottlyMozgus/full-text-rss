package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"full-text-rss/internal/model"
)

// encodeStripSelectors joins strip selectors newline-separated for storage.
func encodeStripSelectors(sels []string) string {
	return strings.Join(sels, "\n")
}

// decodeStripSelectors splits the stored newline-separated strip selectors.
func decodeStripSelectors(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// UpsertSiteRule inserts r or updates the existing row when r.Host already
// has a rule. Unique by host. Sets r.ID and, on insert, r.CreatedAt.
func (s *Store) UpsertSiteRule(ctx context.Context, r *model.SiteRule) (int64, error) {
	now := time.Now().UTC()
	strip := encodeStripSelectors(r.StripSelectors)
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO site_rules (host, content_selector, strip_selectors, url_rewrite_from, url_rewrite_to, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(host) DO UPDATE SET
			content_selector = excluded.content_selector,
			strip_selectors = excluded.strip_selectors,
			url_rewrite_from = excluded.url_rewrite_from,
			url_rewrite_to = excluded.url_rewrite_to,
			updated_at = excluded.updated_at`,
		r.Host, r.ContentSelector, strip, r.URLRewriteFrom, r.URLRewriteTo, now, now)
	if err != nil {
		return 0, fmt.Errorf("upsert site rule %s: %w", r.Host, err)
	}
	if _, err := res.LastInsertId(); err != nil {
		return 0, fmt.Errorf("upsert site rule %s: %w", r.Host, err)
	}
	// Resolve by host, which is unique; on conflict-update LastInsertId is
	// not reliable across drivers.
	var created sql.NullTime
	err = s.db.QueryRowContext(ctx,
		`SELECT id, created_at FROM site_rules WHERE host = ?`, r.Host).
		Scan(&r.ID, &created)
	if err != nil {
		return 0, fmt.Errorf("upsert site rule %s: %w", r.Host, err)
	}
	r.CreatedAt = created.Time
	r.UpdatedAt = now
	return r.ID, nil
}

// DeleteSiteRule removes the rule with the given id.
func (s *Store) DeleteSiteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM site_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete site rule %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete site rule %d: %w", id, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SiteRule returns the rule with the given id, sql.ErrNoRows if missing.
func (s *Store) SiteRule(ctx context.Context, id int64) (*model.SiteRule, error) {
	row := s.db.QueryRowContext(ctx, ruleCols+` WHERE id = ?`, id)
	return scanRule(row.Scan)
}

// SiteRules returns all rules ordered by host.
func (s *Store) SiteRules(ctx context.Context) ([]model.SiteRule, error) {
	rows, err := s.db.QueryContext(ctx, ruleCols+` ORDER BY host`)
	if err != nil {
		return nil, fmt.Errorf("list site rules: %w", err)
	}
	defer rows.Close()
	var out []model.SiteRule
	for rows.Next() {
		r, err := scanRule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("list site rules: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list site rules: %w", err)
	}
	return out, nil
}

// RuleForHost returns the rule matching host by longest suffix: an exact host
// match wins; otherwise the rule whose host is the longest dot-suffix of host
// (rule "example.com" matches "www.example.com"). Returns nil, nil when no
// rule matches.
func (s *Store) RuleForHost(ctx context.Context, host string) (*model.SiteRule, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, nil
	}
	rules, err := s.SiteRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("rule for host %s: %w", host, err)
	}
	var (
		best    *model.SiteRule
		bestLen = -1
	)
	for i := range rules {
		rh := strings.ToLower(strings.TrimSuffix(rules[i].Host, "."))
		if rh == host {
			return &rules[i], nil
		}
		if strings.HasSuffix(host, "."+rh) && len(rh) > bestLen {
			best = &rules[i]
			bestLen = len(rh)
		}
	}
	return best, nil
}

const ruleCols = `SELECT id, host, content_selector, strip_selectors,
	url_rewrite_from, url_rewrite_to, created_at, updated_at FROM site_rules`

// scanRule adapts a row scan into a model.SiteRule.
func scanRule(scan func(dest ...any) error) (*model.SiteRule, error) {
	var (
		r         model.SiteRule
		strip     string
		createdAt sql.NullTime
		updatedAt sql.NullTime
	)
	if err := scan(&r.ID, &r.Host, &r.ContentSelector, &strip,
		&r.URLRewriteFrom, &r.URLRewriteTo, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	r.StripSelectors = decodeStripSelectors(strip)
	r.CreatedAt = createdAt.Time
	r.UpdatedAt = updatedAt.Time
	return &r, nil
}
