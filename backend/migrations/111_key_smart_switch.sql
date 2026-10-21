-- Ordered opt-in routing. Existing keys retain single-group behavior.
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS smart_switch JSONB;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS smart_switch_version BIGINT NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS smart_switch_payg_used DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS smart_switch JSONB;
