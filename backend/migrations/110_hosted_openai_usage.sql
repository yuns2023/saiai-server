-- Opt-in only: do not change groups, balances, historical rows or Key windows.
ALTER TABLE groups ADD COLUMN IF NOT EXISTS hosted_usage_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS metered_cost DECIMAL(20,8);
ALTER TABLE groups ADD CONSTRAINT groups_hosted_usage_policy CHECK (
    NOT hosted_usage_enabled OR
    (platform = 'openai' AND is_exclusive AND subscription_type = 'standard' AND rate_multiplier = 0)
);
-- Enforce new writes without scanning the historical usage table at startup.
ALTER TABLE usage_logs ADD CONSTRAINT usage_logs_metered_cost_nonnegative CHECK (metered_cost >= 0) NOT VALID;
COMMENT ON COLUMN usage_logs.metered_cost IS 'Hosted per-request reference usage; NULL retains historical actual-cost metering';
