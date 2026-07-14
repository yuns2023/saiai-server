CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_session_id_created_at
    ON usage_logs (session_id, created_at DESC)
    WHERE session_id IS NOT NULL;
