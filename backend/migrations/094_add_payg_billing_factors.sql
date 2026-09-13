-- Keep user-facing billing factors separate from upstream/account-cost accounting.
ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS payg_discount_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS model_rate_multipliers JSONB NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS model_rate_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1,
    ADD COLUMN IF NOT EXISTS account_payg_discount_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1;
