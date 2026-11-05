-- Per-key concurrency cap. Every request holds one slot of its key and one of
-- its user; the user's total still caps the sum, so key caps may add up to
-- more than the user's concurrency (first come, first served).
-- A constant default on ADD COLUMN is metadata-only in PostgreSQL 11+.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS max_concurrency integer NOT NULL DEFAULT 5;
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_max_concurrency_range;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_max_concurrency_range CHECK (max_concurrency BETWEEN 1 AND 1000);
