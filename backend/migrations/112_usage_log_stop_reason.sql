-- Record the upstream Anthropic-native stop_reason observed for each usage row.
--
-- Nullable, no default, no backfill, no index: adding the column is a
-- metadata-only change in PostgreSQL, but it still takes an ACCESS EXCLUSIVE
-- lock briefly. lock_timeout makes the migration fail fast instead of queueing
-- behind long-running usage_logs queries.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS stop_reason VARCHAR(32);
