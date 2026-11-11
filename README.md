# rss-scraper

Self-hosted Go service that converts RSS/Atom feeds into **full-text feeds**:
it fetches every article, extracts the main content (Readability + per-site
CSS selector rules), cleans the HTML down to text/links/images, and serves a
private RSS 2.0 feed with `content:encoded`. In the spirit of FiveFilters'
Full-Text RSS, but self-hosted and free.

## Features

- **Saved feeds** with unguessable token URLs: `/feed/<token>.xml` — subscribe from any RSS reader, no login needed for readers.
- **Per-feed item limit**: each saved feed returns its newest 20 entries by default; change it per feed (1 to 500).
- **On-demand conversion**: `/on-demand?url=<feed-url>` converts any feed on the fly.
- **Per-site rules**: content selector, strip selectors, URL rewrite; rules override Readability. Multiple rules per host are allowed and compose (strip selectors union, first content selector wins). Multiple rules per host are allowed and compose: strip selectors union, first content selector and first URL rewrite win.
- **Aggressive HTML cleanup**: no scripts, iframes, classes, or inline styles — safe minimal HTML.
- **Polite fetching**: per-host rate limiting with jitter, so source sites don't ban your IP.
- **Resilient**: extraction failures fall back to the original summary; a bad article never breaks a feed.
- **Admin UI + JSON API** behind password auth; SQLite persistence (one file, easy backup).

## Running

```sh
docker compose -f compose.example.yaml up -d   # or copy to your Dockge stack
```

Required config (env vars):

| Variable | Default | Meaning |
| --- | --- | --- |
| `FTRSS_ADMIN_PASSWORD` | (none) | Admin password. Required unless `FTRSS_ADMIN_TOKEN` is set. |
| `FTRSS_ADMIN_TOKEN` | (none) | Static bearer token (alternative to password). |
| `FTRSS_ADDR` | `0.0.0.0:8080` | Listen address. |
| `FTRSS_DATA_DIR` | `/data` | Where the SQLite file lives. |
| `FTRSS_DEFAULT_REFRESH` | `60m` | Default per-feed refresh interval. |
| `FTRSS_FETCH_TIMEOUT` | `20s` | Per-article fetch timeout. |
| `FTRSS_FETCH_SPACING` | `2s` | Min spacing between fetches to the same host. |
| `FTRSS_FETCH_JITTER` | `3s` | Extra random delay per fetch (0 = off). |
| `FTRSS_USER_AGENT` | `rss-scraper/1.0 ...` | User-Agent for fetches. |
| `FTRSS_FALLBACK_IMAGE` | (none) | Absolute `http(s)` image URL used for items that have no image of their own. When unset, a built-in placeholder is served from `/static/img/fallback.png`. |

## Local development

```sh
go build ./...
go test ./...
FTRSS_ADMIN_PASSWORD=dev ./rss-scraper
```

## Routes

- `/login`, `/logout` — admin session
- `/admin/feeds` — manage saved feeds (add, edit, refresh now, status per item)
- `/admin/rules` — per-site extraction rules
- `/admin/ondemand` — try the on-demand converter
- `/feed/<token>.xml` — the full-text feed (token is the auth)
- `/on-demand?url=...` — on-demand conversion
- `/healthz` — liveness
- `/api/...` — JSON API (same auth as UI)

## Limiting feed size

Each saved feed has a **Max items** setting: how many of the newest entries
`/feed/<token>.xml` returns. The default is 20 and the allowed range is 1 to 500.

- Admin UI: the "Max items" field on the add and edit feed forms.
- JSON API: `max_items` on `POST /api/feeds` and `PUT /api/feeds/{id}`. Leaving
  it out (or sending 0) uses 20 on create and keeps the current value on update.

```sh
curl -H "Authorization: Bearer $FTRSS_ADMIN_TOKEN" -H "Content-Type: application/json" \
  -X PUT -d '{"source_url":"https://example.com/feed.xml","max_items":50}' \
  http://localhost:8080/api/feeds/1
```

The limit only affects what the feed URL returns. Older items stay cached, so
raising the limit later brings them back. The on-demand endpoint keeps its own
fixed cap of 10 items.

Upgrade note: before this setting existed, saved feeds returned up to 100
entries. After upgrading, existing feeds return 20 until you raise their limit.

## Backups

Everything lives in one SQLite file (`$FTRSS_DATA_DIR/fulltextrss.db`).
Stop the container (or use `sqlite3 .backup`) and copy it.
