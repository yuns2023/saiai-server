-- PAYG discounts belong to SAIAI users, not upstream provider accounts.
-- Keep the legacy account and usage-log columns for rollback compatibility and
-- historical bill explanation; new billing uses the user-owned fields below.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS payg_discount_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1;

ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS user_payg_discount_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1;
