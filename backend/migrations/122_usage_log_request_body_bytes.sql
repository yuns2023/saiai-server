-- Record the inbound client request payload size for each usage row so admins
-- can find users / API keys that send very large requests. Admin diagnostics
-- only: billing, fingerprints and idempotency do not use this column.
--
-- NULL means unknown (rows written before this migration, or a path without a
-- trustworthy size). 0 is only written for a genuinely empty body.
--
-- Nullable, no default, no backfill, no index: adding the column is a
-- metadata-only change in PostgreSQL, but it still takes an ACCESS EXCLUSIVE
-- lock briefly. lock_timeout makes the migration fail fast instead of queueing
-- behind long-running usage_logs queries.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS request_body_bytes BIGINT;
