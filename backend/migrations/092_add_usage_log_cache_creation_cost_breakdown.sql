-- Preserve the exact 5-minute and 1-hour cache creation costs calculated for
-- each request. Existing cache_creation_cost remains the canonical aggregate.
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS cache_creation_5m_cost DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS cache_creation_1h_cost DECIMAL(20, 10) NOT NULL DEFAULT 0;
