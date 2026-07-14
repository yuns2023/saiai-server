ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS session_id VARCHAR(255);

COMMENT ON COLUMN usage_logs.session_id IS 'Original client session identifier captured from request headers/body/metadata for usage-log correlation.';
