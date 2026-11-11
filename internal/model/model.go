// Package model defines the persistent domain types shared by store,
// pipeline, and server layers.
package model

import (
	"database/sql"
	"time"
)

// Bounds for Feed.MaxItems. DefaultMaxItems must match the column default
// in store/migrations/002_feed_max_items.sql.
const (
	DefaultMaxItems = 20
	MaxItemsCeiling = 500
)

// Feed is a saved feed configuration.
type Feed struct {
	ID             int64
	Token          string // unguessable, used in /feed/<token>.xml
	SourceURL      string // RSS/Atom feed to convert
	Title          string // optional display title
	RefreshEvery   time.Duration
	MaxItems       int    // entries served at /feed/<token>.xml; 0 on write means DefaultMaxItems
	LastRefresh    sql.NullTime
	LastStatus     string // "ok" | "error"
	LastError      string
	CreatedAt      time.Time
}

// SiteRule holds per-site extraction overrides.
type SiteRule struct {
	ID          int64
	Host        string // e.g. example.com (matched by suffix on host)
	ContentSelector string        // CSS selector for main content
	StripSelectors  []string      // CSS selectors removed before/after extraction
	URLRewriteFrom  string        // substring rewrite on article URL (optional)
	URLRewriteTo    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Item is a cached feed item with extraction result.
type Item struct {
	ID          int64
	FeedID      int64
	GUID        string // original item GUID/link, dedupe key
	URL         string
	Title       string
	Author      string
	PublishedAt sql.NullTime
	Categories  []string
	Summary     string // original summary/description
	ContentHTML string // extracted cleaned HTML; empty if extraction failed
	Extracted   bool   // true if extraction was attempted and succeeded
	Failed      bool   // true if extraction was attempted and failed
	FetchedAt   sql.NullTime
}

// ItemStatus summarizes per-item extraction state for the UI status view.
type ItemStatus struct {
	GUID    string
	Title   string
	State   string // "extracted" | "summary-fallback" | "pending"
	Details string // error message when failed
}
