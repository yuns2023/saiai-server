-- Optional, allowlisted diagnostic snapshots; historical rows stay NULL.
ALTER TABLE ops_error_logs ADD COLUMN IF NOT EXISTS diagnostics JSONB;
