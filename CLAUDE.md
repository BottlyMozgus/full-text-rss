> **Working spec:** `/workspace/interview/full-text-rss-feed-generator-k7d2f4.md` (status: complete).
> Read it before implementing. The summary below is context only; the spec wins on conflicts.

# full-text-rss

Self-hosted Go service (single binary) converting RSS/Atom feeds into
full-text feeds. Saved feeds are served privately at `/feed/<token>.xml`;
an on-demand endpoint converts any feed URL FiveFilters-style. Admin UI +
JSON API behind password auth. SQLite persistence, background scheduler
with per-host rate limiting.

## Layout

```
cmd/full-text-rss/     main: config load, store open, server + scheduler start
internal/config/       env-var config (DONE - see config.go)
internal/model/        domain types: Feed, SiteRule, Item (DONE)
internal/store/        SQLite (modernc.org/sqlite, pure Go) + migrations/
internal/feedio/       gofeed parse -> RSS 2.0 + content:encoded output
internal/extract/      fetcher (rate-limited) + readability + rules + cleanup
internal/server/       HTTP handlers: admin UI, API, feed token URLs
internal/scheduler/    per-feed refresh loop, jitter, new-items-only
web/static/            css + minimal vanilla js
```

## Interface contracts (must match — server/pipeline are built against these)

```go
// store: all methods take ctx first. Errors wrapped with %w.
type Store struct{ /* ... */ }
func Open(path string) (*Store, error)                       // DONE
func (s *Store) Close() error                                // DONE
func (s *Store) CreateFeed(ctx, f *model.Feed) (int64, error) // generates token if empty
func (s *Store) UpdateFeed(ctx, f *model.Feed) error
func (s *Store) DeleteFeed(ctx, id int64) error              // cascades items
func (s *Store) Feed(ctx, id int64) (*model.Feed, error)     // sql.ErrNoRows if missing
func (s *Store) FeedByToken(ctx, token string) (*model.Feed, error)
func (s *Store) Feeds(ctx) ([]model.Feed, error)
func (s *Store) DueFeeds(ctx, now time.Time) ([]model.Feed, error) // last_refresh null or older than refresh_every
func (s *Store) MarkFeedRefreshed(ctx, id int64, status, errMsg string, now time.Time) error
func (s *Store) UpsertSiteRule(ctx, r *model.SiteRule) (int64, error) // unique by host
func (s *Store) DeleteSiteRule(ctx, id int64) error
func (s *Store) SiteRule(ctx, id int64) (*model.SiteRule, error)
func (s *Store) SiteRules(ctx) ([]model.SiteRule, error)
func (s *Store) RuleForHost(ctx, host string) (*model.SiteRule, error) // longest-suffix match
func (s *Store) UpsertItem(ctx, feedID int64, it *model.Item) (isNew bool, err error) // dedupe on (feed_id,guid)
func (s *Store) ItemsForFeed(ctx, feedID int64, limit int) ([]model.Item, error) // newest first
func (s *Store) PendingItems(ctx, feedID int64) ([]model.Item, error) // extracted=false
func (s *Store) Item(ctx, id int64) (*model.Item, error)
func (s *Store) UpdateItemContent(ctx, id int64, html string, extracted, failed bool) error
```

```go
// extract
type Fetcher struct{ /* per-host rate limiter, timeouts, UA */ }
func NewFetcher(cfg *config.Config) *Fetcher
func (f *Fetcher) Fetch(ctx, url string) ([]byte, error)     // rate-limited GET, 200 only
type Extractor struct{ /* fetcher + rules */ }
func NewExtractor(f *Fetcher, rules RuleSource) *Extractor
// Result.ContentHTML is cleaned minimal HTML ("" + ErrExtract on failure)
func (e *Extractor) Extract(ctx, articleURL string) (Result, error)
type RuleSource interface {
    RuleForHost(ctx context.Context, host string) (*model.SiteRule, error)
}
```

```go
// feedio
type ItemContent struct{ Item *gofeed.Item; HTML string; Fallback bool } // DONE
type Output struct{ Title, Link, Description string; Items []ItemContent; LastModified time.Time } // DONE
// NOTE: encoder.go ToRSS is a STUB — implement real RSS 2.0 XML marshaling
// (build the XML tree directly or hand-roll writer; content:encoded in the
// content namespace per item; description = original summary; correct
// Content-Type application/rss+xml handled by server).
```

```go
// server (routes)
GET  /login                      POST /login                    POST /logout
GET  /admin/feeds                GET/POST /admin/feeds/new
GET/POST /admin/feeds/{id}/edit  POST /admin/feeds/{id}/delete
POST /admin/feeds/{id}/refresh   GET  /admin/feeds/{id}         (status + items)
GET  /admin/feeds/{id}/items/{itemId}  (preview)
GET/POST /admin/rules            GET/POST /admin/rules/{id}/edit
POST /admin/rules/{id}/delete
GET  /admin/ondemand             GET /on-demand?url=...
GET  /feed/{token}.xml           (NO auth — token IS the auth; 404 on miss)
GET  /healthz                    (no auth)
// JSON API under /api/ (same auth): feeds CRUD, rules CRUD,
// POST /api/feeds/{id}/refresh, GET /api/feeds/{id}/status
```

## Decisions already made
- modernc.org/sqlite (pure Go, no cgo) — required for scratch-ish Docker builds.
- HTML cleanup: bluemonday with strict allowlist: p, br, a[href abs-urls],
  img[src alt], h1-h6, ul/ol/li, blockquote, pre, code, strong, em, table set.
  No classes/ids/styles/scripts/iframes.
- Tokens: crypto/rand 32 hex chars.
- Readability lib: use github.com/go-shiori/go-readability.
- Scheduler: single goroutine ticking every minute, claims due feeds, jittered
  stagger; per-host limiter in extract.Fetcher (spacing 2s + jitter, per config).
- On-demand: parse feed, extract up to ~10 newest items inline with short
  timeout, stream XML, no persistence.
- Session auth: cookie (HMAC-signed token) after password POST; constant-time
  compare; also accept `Authorization: Bearer <FTRSS_ADMIN_TOKEN>` if set.

## Verification (each lane owns its package's `go test`; I run the full suite)
- feedio: RSS+Atom fixture round-trip -> valid RSS 2.0 with content:encoded.
- extract: local httptest HTML pages, rule override wins, cleanup strips
  scripts/classes/styles, fallback on garbage/PDF.
- store: temp-dir DB, CRUD + dedupe + DueFeeds.
- server: httptest, auth enforcement, token feed 404 without valid token.
- End-to-end (mine): build, run against local fixture feeds, curl the XML.

Fixer lanes own `go build ./... && go test ./<pkg>/...` for their packages.
