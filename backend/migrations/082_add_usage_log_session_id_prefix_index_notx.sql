CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_session_id_prefix_created_at
    ON usage_logs (session_id text_pattern_ops, created_at DESC)
    WHERE session_id IS NOT NULL;
