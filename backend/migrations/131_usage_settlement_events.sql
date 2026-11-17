-- Shadow settlement events (P2b). While billing commits, the usage billing
-- repository tries to write one row per completed sale that it identifies at
-- settlement time as hosted (the priced group uses hosted metering) or as
-- served by an owned account. Each row freezes the classification, fee rate
-- and the charge input handed to the billing effects.
--
-- Shadow only: rows never produce money (owner_earning is pinned to 0) and are
-- never a source of owner earnings. The write is best effort inside the billing
-- transaction (savepoint): a failed event write never blocks the charge, so a
-- sale may have no event. Replays that the dedup claim skips never backfill a
-- missing event. The admin reconcile endpoint finds missing rows. P3 earnings
-- will only use rows with owner_source = 'admission'.
--
-- owner_user_id is accounts.owner_user_id read inside the billing transaction
-- (owner_source = 'settlement'), not an admission-time snapshot.
-- charge_input is the same float handed to the effects; users.balance is
-- DECIMAL(20,8) (pre-existing), so it is not the exact balance change.
-- invalid_fields lists event amounts that were not finite, negative or out of
-- numeric(20,10) range and were recorded as 0; such rows are excluded from
-- amount reconciliation.
--
-- No foreign keys (history survives account/user deletes, like usage_logs).
-- No update/delete path; retention is permanent (not cleaned with usage_logs
-- or the dedup tables). The table is new and empty, so its indexes are built
-- in this transaction.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS usage_settlement_events (
    id BIGSERIAL PRIMARY KEY,
    request_id TEXT NOT NULL,
    api_key_id BIGINT NOT NULL,
    category TEXT NOT NULL,
    rule_version SMALLINT NOT NULL,
    anomaly BOOLEAN NOT NULL DEFAULT FALSE,
    buyer_user_id BIGINT NOT NULL,
    account_id BIGINT NOT NULL,
    owner_user_id BIGINT,
    owner_source TEXT NOT NULL,
    self_purchase BOOLEAN NOT NULL,
    group_id BIGINT,
    original_group_id BIGINT,
    fee_rate NUMERIC(20,10),
    standard_cost NUMERIC(20,10) NOT NULL,
    charge_input NUMERIC(20,10) NOT NULL,
    charge_source TEXT NOT NULL,
    subscription_id BIGINT,
    zero_reason TEXT,
    invalid_fields TEXT[],
    owner_earning NUMERIC(20,10) NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT usage_settlement_events_identity_key UNIQUE (request_id, api_key_id),
    CONSTRAINT usage_settlement_events_category_check CHECK (category IN ('hosted_self', 'owned_other')),
    CONSTRAINT usage_settlement_events_owner_source_check CHECK (owner_source IN ('settlement', 'admission')),
    CONSTRAINT usage_settlement_events_charge_source_check CHECK (charge_source IN ('balance', 'subscription', 'none')),
    CONSTRAINT usage_settlement_events_zero_reason_check CHECK (zero_reason IN ('free_group', 'missing_price', 'zero_usage', 'invalid_amount', 'other')),
    CONSTRAINT usage_settlement_events_owner_earning_check CHECK (owner_earning = 0),
    CONSTRAINT usage_settlement_events_amounts_check CHECK (
        standard_cost >= 0 AND charge_input >= 0 AND (fee_rate IS NULL OR fee_rate >= 0)
    )
);

CREATE INDEX IF NOT EXISTS usage_settlement_events_owner_created_idx
    ON usage_settlement_events (owner_user_id, created_at);
CREATE INDEX IF NOT EXISTS usage_settlement_events_created_idx
    ON usage_settlement_events (created_at);
