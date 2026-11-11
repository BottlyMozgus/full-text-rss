-- 004 repair time values written by drivers before _time_format=sqlite was
-- configured (repository change 2026-11).
--
-- Background: modernc sqlite serializes time.Time via time.Time.String() by
-- default, e.g. "2026-11-11 18:32:16 +0100 CET". That round-trips through the
-- driver's read-side parser only when the zone has a resolvable name. Feed
-- dates parsed from numeric UTC offsets (rss.orf.at dc:date, +01:00) can
-- carry a zone with an EMPTY name, producing strings like
-- "2026-11-11 18:32:16 +0100 +0100" which no later scan into time.Time can
-- read back. Such rows broke every ItemsForFeed/PendingItems/Item read for
-- their feed.
--
-- The DSN now writes the ISO layout "2006-01-02 15:04:05.999999999-07:00",
-- which the driver always parses. This migration rewrites the legacy broken
-- strings (offset duplicated after a space) into that layout. Values with a
-- named zone already parse and are left untouched; mixed old/new formats can
-- briefly order slightly off in published_at sorting and converge as feeds
-- refresh (upserts rewrite the column).
--
-- GLOB breaks down: trailing "<space><digit> [+-]HHMM [+-]HHMM". Named-zone
-- strings end in letters and never match. substr math for
-- p = "2026-11-11 18:32:16 +0100 +0100": keep 1..len-12, then "+01" from
-- len-4..len-2, a colon, and "00" from len-1..len.
UPDATE items SET published_at =
    substr(published_at, 1, length(published_at) - 12)
    || substr(published_at, length(published_at) - 4, 3)
    || ':'
    || substr(published_at, length(published_at) - 1, 2)
WHERE published_at GLOB '*[0-9] [+-][0-9][0-9][0-9][0-9] [+-][0-9][0-9][0-9][0-9]';

UPDATE items SET fetched_at =
    substr(fetched_at, 1, length(fetched_at) - 12)
    || substr(fetched_at, length(fetched_at) - 4, 3)
    || ':'
    || substr(fetched_at, length(fetched_at) - 1, 2)
WHERE fetched_at GLOB '*[0-9] [+-][0-9][0-9][0-9][0-9] [+-][0-9][0-9][0-9][0-9]';

UPDATE feeds SET last_refresh =
    substr(last_refresh, 1, length(last_refresh) - 12)
    || substr(last_refresh, length(last_refresh) - 4, 3)
    || ':'
    || substr(last_refresh, length(last_refresh) - 1, 2)
WHERE last_refresh GLOB '*[0-9] [+-][0-9][0-9][0-9][0-9] [+-][0-9][0-9][0-9][0-9]';

UPDATE feeds SET created_at =
    substr(created_at, 1, length(created_at) - 12)
    || substr(created_at, length(created_at) - 4, 3)
    || ':'
    || substr(created_at, length(created_at) - 1, 2)
WHERE created_at GLOB '*[0-9] [+-][0-9][0-9][0-9][0-9] [+-][0-9][0-9][0-9][0-9]';
