-- 003 allow multiple rules per host: drop the unique index; a plain index
-- keeps host lookups fast. Extraction composes the matching rules.
DROP INDEX IF EXISTS idx_site_rules_host;
CREATE INDEX IF NOT EXISTS idx_site_rules_host_lookup ON site_rules(host);
