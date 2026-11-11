package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"rss-scraper/internal/model"
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

// CreateSiteRule inserts r as a new row. Multiple rules per host are
// allowed; extraction composes them. Sets r.ID and r.CreatedAt.
func (s *Store) CreateSiteRule(ctx context.Context, r *model.SiteRule) (int64, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO site_rules (host, content_selector, strip_selectors, url_rewrite_from, url_rewrite_to, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Host, r.ContentSelector, encodeStripSelectors(r.StripSelectors),
		r.URLRewriteFrom, r.URLRewriteTo, now, now)
	if err != nil {
		return 0, fmt.Errorf("create site rule %s: %w", r.Host, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create site rule %s: %w", r.Host, err)
	}
	r.ID = id
	r.CreatedAt = now
	r.UpdatedAt = now
	return id, nil
}

// UpdateSiteRule writes r over the row with r.ID. Sets r.UpdatedAt. Returns
// sql.ErrNoRows when the id does not exist. The host may change.
func (s *Store) UpdateSiteRule(ctx context.Context, r *model.SiteRule) error {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		`UPDATE site_rules SET host = ?, content_selector = ?, strip_selectors = ?,
			url_rewrite_from = ?, url_rewrite_to = ?, updated_at = ?
		 WHERE id = ?`,
		r.Host, r.ContentSelector, encodeStripSelectors(r.StripSelectors),
		r.URLRewriteFrom, r.URLRewriteTo, now, r.ID)
	if err != nil {
		return fmt.Errorf("update site rule %d: %w", r.ID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update site rule %d: %w", r.ID, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	r.UpdatedAt = now
	return nil
}

// UpsertSiteRule keeps the historical by-host upsert for callers that
// address rules by host: the first rule for that host is updated, or a new
// row is created when the host has none yet.
func (s *Store) UpsertSiteRule(ctx context.Context, r *model.SiteRule) (int64, error) {
	existing, err := s.firstRuleForHost(ctx, r.Host)
	if err != nil {
		return 0, err
	}
	if existing == nil {
		return s.CreateSiteRule(ctx, r)
	}
	r.ID = existing.ID
	r.CreatedAt = existing.CreatedAt
	if err := s.UpdateSiteRule(ctx, r); err != nil {
		return 0, err
	}
	r.UpdatedAt = time.Now().UTC()
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

// RulesForHost returns every rule whose host is the host itself or a dot
// suffix of it (rule "example.com" matches "www.example.com"), longest
// suffix first, exact host matches first within the same suffix. Empty when
// nothing matches.
func (s *Store) RulesForHost(ctx context.Context, host string) ([]model.SiteRule, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, nil
	}
	rules, err := s.SiteRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("rules for host %s: %w", host, err)
	}
	var out []model.SiteRule
	for i := range rules {
		rh := strings.ToLower(strings.TrimSuffix(rules[i].Host, "."))
		if rh == host || strings.HasSuffix(host, "."+rh) {
			out = append(out, rules[i])
		}
	}
	// Longest rule host first so more specific rules apply later (their
	// strip selectors run after broader ones) and win tie-breaks.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(strings.ToLower(out[j].Host)) > len(strings.ToLower(out[j-1].Host)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// firstRuleForHost returns the first rule whose host equals host exactly,
// or nil when the host has no exact rule. Used by the by-host upsert.
func (s *Store) firstRuleForHost(ctx context.Context, host string) (*model.SiteRule, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	rules, err := s.SiteRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("rule for host %s: %w", host, err)
	}
	for i := range rules {
		if strings.ToLower(strings.TrimSuffix(rules[i].Host, ".")) == host {
			return &rules[i], nil
		}
	}
	return nil, nil
}

// RuleForHost composes every rule matching host into one effective rule:
// union of strip selectors in (longest-suffix-first) rule order, the first
// non-empty content selector, and the first URL rewrite. Returns nil, nil
// when no rule matches.
func (s *Store) RuleForHost(ctx context.Context, host string) (*model.SiteRule, error) {
	rules, err := s.RulesForHost(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, nil
	}
	composed := &model.SiteRule{Host: rules[0].Host}
	for i := range rules {
		r := &rules[i]
		if composed.ContentSelector == "" {
			composed.ContentSelector = r.ContentSelector
		}
		composed.StripSelectors = append(composed.StripSelectors, r.StripSelectors...)
		if composed.URLRewriteFrom == "" && r.URLRewriteFrom != "" {
			composed.URLRewriteFrom = r.URLRewriteFrom
			composed.URLRewriteTo = r.URLRewriteTo
		}
	}
	return composed, nil
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
