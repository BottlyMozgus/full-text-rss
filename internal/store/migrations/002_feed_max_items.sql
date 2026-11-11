-- 002 per-feed cap on entries served at /feed/<token>.xml.
-- The default must match model.DefaultMaxItems.
ALTER TABLE feeds ADD COLUMN max_items INTEGER NOT NULL DEFAULT 20;
