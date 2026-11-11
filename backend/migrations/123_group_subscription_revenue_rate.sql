-- Statistics-only conversion rate for subscription groups: how many yuan the
-- site receives per $1 of subscription quota consumed (e.g. 0.64). Billing,
-- quotas and charges never read it; admin usage summaries, rankings and
-- account window stats multiply historical subscription consumption by the
-- group's CURRENT rate, so changing it re-prices past periods by design.
--
-- NULL = not configured (reported separately as unpriced consumption);
-- 0 = explicitly free. groups is a small table, so the brief lock is cheap.

SET LOCAL lock_timeout = '5s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS subscription_revenue_rate DECIMAL(10,4);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_subscription_revenue_rate_check'
          AND conrelid = 'groups'::regclass
    ) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_subscription_revenue_rate_check
            CHECK (subscription_revenue_rate IS NULL OR (subscription_revenue_rate >= 0 AND subscription_revenue_rate <= 1000));
    END IF;
END $$;
