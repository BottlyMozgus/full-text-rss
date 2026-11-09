-- 001 initial schema
CREATE TABLE IF NOT EXISTS feeds (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    token         TEXT NOT NULL UNIQUE,
    source_url    TEXT NOT NULL,
    title         TEXT NOT NULL DEFAULT '',
    refresh_every_seconds INTEGER NOT NULL DEFAULT 3600,
    last_refresh  TIMESTAMP NULL,
    last_status   TEXT NOT NULL DEFAULT '',
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS site_rules (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    host             TEXT NOT NULL,
    content_selector TEXT NOT NULL DEFAULT '',
    strip_selectors  TEXT NOT NULL DEFAULT '', -- newline-separated
    url_rewrite_from TEXT NOT NULL DEFAULT '',
    url_rewrite_to   TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_site_rules_host ON site_rules(host);

CREATE TABLE IF NOT EXISTS items (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    feed_id      INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
    guid         TEXT NOT NULL,
    url          TEXT NOT NULL DEFAULT '',
    title        TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    published_at TIMESTAMP NULL,
    categories   TEXT NOT NULL DEFAULT '', -- comma-separated
    summary      TEXT NOT NULL DEFAULT '',
    content_html TEXT NOT NULL DEFAULT '',
    extracted    INTEGER NOT NULL DEFAULT 0,
    failed       INTEGER NOT NULL DEFAULT 0,
    fetched_at   TIMESTAMP NULL,
    UNIQUE(feed_id, guid)
);
CREATE INDEX IF NOT EXISTS idx_items_feed ON items(feed_id);
