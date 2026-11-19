-- Hosted sharing market (P3a), schema only. Every change is additive and safe
-- while the feature switch (setting market_enabled) is off; nothing here is
-- read by scheduling or billing yet.
--
-- groups.market_tier: an admin-designated sharing tier. Only standard
-- (balance-charged), non-hosted, positively priced anthropic groups qualify,
-- so every tier sale has an actual charge to split.
--
-- account_groups.source: 'admin' rows are the existing admin bindings;
-- 'market' rows are written only by the listing lifecycle (owner lists the
-- account into the tier the admin allowed). The primary key stays
-- (account_id, group_id): one row per pair, so an admin binding and a market
-- listing of the same pair are an explicit conflict, never two rows.
--
-- account_market_listings: one row per account. allowed_group_ids is set by
-- the admin, selected_group_id/status by the owner. version increases on
-- every change (admission records pin it). Owner-protection parameters are
-- columns (not accounts.extra) so whole-row account write-backs can never
-- restore an old value. account_market_listing_events is the append-only
-- history (one row per version).
--
-- market_admissions: immutable record of an authoritative market admission
-- (written by the scheduler in a later PR). owner_event_id pins the owner
-- version (account_owner_events.id). No foreign keys to users or accounts so
-- the billing transaction never takes KEY SHARE locks on those rows and the
-- history survives deletes.
--
-- usage_settlement_events: adds the 'shared_sale' category and the market
-- money columns. Only shared_sale rows may carry a positive owner earning;
-- the P2b shadow categories stay pinned to 0. Existing rows satisfy every
-- new constraint (owner_earning = 0, new columns NULL).
--
-- owner_earning_wallets / owner_earning_entries: owner earnings ledger. The
-- entries table is insert-only (an UPDATE raises); accrue > 0, reverse and
-- convert < 0; one accrue per settlement event, one reverse per accrue, one
-- convert per (owner, request id).
--
-- redeem_codes has no CHECK on type, so the 'market_earning' history type
-- needs no change there.
--
-- All new tables are empty, so their indexes are built in this transaction.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS market_tier BOOLEAN NOT NULL DEFAULT FALSE;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'groups_market_tier_check') THEN
        ALTER TABLE groups ADD CONSTRAINT groups_market_tier_check CHECK (
            NOT market_tier OR (
                subscription_type = 'standard'
                AND hosted_usage_enabled = FALSE
                AND rate_multiplier > 0
                AND platform = 'anthropic'
            )
        );
    END IF;
END $$;

ALTER TABLE account_groups ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'admin';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'account_groups_source_check') THEN
        ALTER TABLE account_groups ADD CONSTRAINT account_groups_source_check
            CHECK (source IN ('admin', 'market'));
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS account_market_listings (
    account_id             BIGINT PRIMARY KEY,
    owner_user_id          BIGINT NOT NULL,
    allowed_group_ids      BIGINT[] NOT NULL DEFAULT '{}',
    selected_group_id      BIGINT,
    status                 TEXT NOT NULL DEFAULT 'unlisted',
    version                BIGINT NOT NULL DEFAULT 1,
    reserved_concurrency   INT NOT NULL DEFAULT 1,
    reserved_devices       INT NOT NULL DEFAULT 2,
    reserved_admissions    INT NOT NULL DEFAULT 1,
    cap_5h_pct             SMALLINT NOT NULL DEFAULT 70,
    cap_7d_pct             SMALLINT NOT NULL DEFAULT 60,
    sample_max_age_minutes INT NOT NULL DEFAULT 30,
    listed_at              TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT account_market_listings_status_check
        CHECK (status IN ('unlisted', 'listed', 'revoked')),
    CONSTRAINT account_market_listings_version_check CHECK (version >= 1),
    CONSTRAINT account_market_listings_allowed_check CHECK (
        cardinality(allowed_group_ids) <= 32
        AND array_position(allowed_group_ids, NULL) IS NULL
    ),
    -- Listed ⇒ the selected tier is one the admin allowed.
    CONSTRAINT account_market_listings_listed_check CHECK (
        status <> 'listed'
        OR (selected_group_id IS NOT NULL AND selected_group_id = ANY (allowed_group_ids) AND listed_at IS NOT NULL)
    ),
    -- Revoked ⇒ nothing allowed or selected.
    CONSTRAINT account_market_listings_revoked_check CHECK (
        status <> 'revoked' OR (cardinality(allowed_group_ids) = 0 AND selected_group_id IS NULL)
    ),
    CONSTRAINT account_market_listings_params_check CHECK (
        reserved_concurrency BETWEEN 0 AND 100
        AND reserved_devices BETWEEN 0 AND 32
        AND reserved_admissions BETWEEN 0 AND 32
        AND cap_5h_pct BETWEEN 1 AND 100
        AND cap_7d_pct BETWEEN 1 AND 100
        AND sample_max_age_minutes BETWEEN 1 AND 1440
    )
);

CREATE INDEX IF NOT EXISTS account_market_listings_owner_idx
    ON account_market_listings (owner_user_id);
CREATE INDEX IF NOT EXISTS account_market_listings_selected_idx
    ON account_market_listings (selected_group_id) WHERE status = 'listed';

CREATE TABLE IF NOT EXISTS account_market_listing_events (
    id                     BIGSERIAL PRIMARY KEY,
    account_id             BIGINT NOT NULL,
    version                BIGINT NOT NULL,
    action                 TEXT NOT NULL,
    actor_role             TEXT NOT NULL,
    actor_user_id          BIGINT,
    owner_user_id          BIGINT NOT NULL,
    status                 TEXT NOT NULL,
    allowed_group_ids      BIGINT[] NOT NULL,
    selected_group_id      BIGINT,
    reserved_concurrency   INT NOT NULL,
    reserved_devices       INT NOT NULL,
    reserved_admissions    INT NOT NULL,
    cap_5h_pct             SMALLINT NOT NULL,
    cap_7d_pct             SMALLINT NOT NULL,
    sample_max_age_minutes INT NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT account_market_listing_events_version_key UNIQUE (account_id, version),
    CONSTRAINT account_market_listing_events_actor_role_check
        CHECK (actor_role IN ('admin', 'owner', 'system')),
    CONSTRAINT account_market_listing_events_status_check
        CHECK (status IN ('unlisted', 'listed', 'revoked'))
);

CREATE TABLE IF NOT EXISTS market_admissions (
    id              BIGSERIAL PRIMARY KEY,
    attempt_id      TEXT NOT NULL,
    account_id      BIGINT NOT NULL,
    group_id        BIGINT NOT NULL,
    owner_user_id   BIGINT NOT NULL,
    owner_event_id  BIGINT NOT NULL,
    listing_version BIGINT NOT NULL,
    buyer_user_id   BIGINT NOT NULL,
    api_key_id      BIGINT NOT NULL,
    admitted_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT market_admissions_attempt_key UNIQUE (attempt_id),
    CONSTRAINT market_admissions_attempt_check CHECK (attempt_id <> '' AND length(attempt_id) <= 128),
    CONSTRAINT market_admissions_refs_check CHECK (
        account_id > 0 AND group_id > 0 AND owner_user_id > 0 AND owner_event_id > 0
        AND listing_version >= 1 AND buyer_user_id > 0 AND api_key_id > 0
    )
);

CREATE INDEX IF NOT EXISTS market_admissions_account_idx
    ON market_admissions (account_id, admitted_at);
CREATE INDEX IF NOT EXISTS market_admissions_owner_idx
    ON market_admissions (owner_user_id, admitted_at);

-- Insert-only tables: an UPDATE is always a bug.
CREATE OR REPLACE FUNCTION market_reject_update() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is insert-only', TG_TABLE_NAME USING ERRCODE = 'restrict_violation';
END $$;

DROP TRIGGER IF EXISTS market_admissions_no_update ON market_admissions;
CREATE TRIGGER market_admissions_no_update
    BEFORE UPDATE ON market_admissions
    FOR EACH ROW EXECUTE FUNCTION market_reject_update();

ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS admission_id BIGINT;
ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS charged_amount NUMERIC(20,10);
ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS platform_amount NUMERIC(20,10);
ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS platform_cut_rate NUMERIC(6,4);
ALTER TABLE usage_settlement_events ADD COLUMN IF NOT EXISTS earning_zero_reason TEXT;

ALTER TABLE usage_settlement_events DROP CONSTRAINT IF EXISTS usage_settlement_events_category_check;
ALTER TABLE usage_settlement_events ADD CONSTRAINT usage_settlement_events_category_check
    CHECK (category IN ('hosted_self', 'owned_other', 'shared_sale'));

ALTER TABLE usage_settlement_events DROP CONSTRAINT IF EXISTS usage_settlement_events_owner_earning_check;
ALTER TABLE usage_settlement_events ADD CONSTRAINT usage_settlement_events_owner_earning_check
    CHECK (owner_earning >= 0 AND (owner_earning = 0 OR category = 'shared_sale'));

ALTER TABLE usage_settlement_events DROP CONSTRAINT IF EXISTS usage_settlement_events_market_check;
ALTER TABLE usage_settlement_events ADD CONSTRAINT usage_settlement_events_market_check CHECK (
    -- Market columns only on shared_sale rows.
    (category = 'shared_sale' OR (
        admission_id IS NULL AND charged_amount IS NULL AND platform_amount IS NULL
        AND platform_cut_rate IS NULL AND earning_zero_reason IS NULL
    ))
    -- A shared_sale row always comes from an admission and splits the
    -- actual charge exactly: charged = owner earning + platform amount.
    AND (category <> 'shared_sale' OR (
        admission_id IS NOT NULL AND charged_amount IS NOT NULL
        AND platform_amount IS NOT NULL AND platform_cut_rate IS NOT NULL
        AND charged_amount >= 0 AND platform_amount >= 0
        AND owner_earning <= charged_amount
        AND charged_amount = owner_earning + platform_amount
        AND platform_cut_rate >= 0 AND platform_cut_rate <= 1
        -- A zero earning always states why.
        AND (owner_earning > 0 OR earning_zero_reason IS NOT NULL)
    ))
);

ALTER TABLE usage_settlement_events DROP CONSTRAINT IF EXISTS usage_settlement_events_earning_zero_reason_check;
ALTER TABLE usage_settlement_events ADD CONSTRAINT usage_settlement_events_earning_zero_reason_check
    CHECK (earning_zero_reason IN ('self_purchase', 'zero_charge', 'owner_mismatch', 'not_eligible', 'other'));

CREATE UNIQUE INDEX IF NOT EXISTS usage_settlement_events_admission_key
    ON usage_settlement_events (admission_id) WHERE admission_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS owner_earning_wallets (
    user_id    BIGINT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS owner_earning_entries (
    id                  BIGSERIAL PRIMARY KEY,
    owner_user_id       BIGINT NOT NULL,
    kind                TEXT NOT NULL,
    amount              NUMERIC(20,10) NOT NULL,
    settlement_event_id BIGINT REFERENCES usage_settlement_events(id),
    reverses_entry_id   BIGINT REFERENCES owner_earning_entries(id),
    available_at        TIMESTAMPTZ,
    convert_request_id  TEXT,
    admin_user_id       BIGINT,
    note                TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT owner_earning_entries_kind_check CHECK (kind IN ('accrue', 'reverse', 'convert')),
    CONSTRAINT owner_earning_entries_sign_check CHECK (
        (kind = 'accrue' AND amount > 0) OR (kind IN ('reverse', 'convert') AND amount < 0)
    ),
    CONSTRAINT owner_earning_entries_shape_check CHECK (
        (kind = 'accrue' AND settlement_event_id IS NOT NULL AND reverses_entry_id IS NULL
            AND available_at IS NOT NULL AND convert_request_id IS NULL)
        OR (kind = 'reverse' AND reverses_entry_id IS NOT NULL
            AND available_at IS NOT NULL AND convert_request_id IS NULL)
        OR (kind = 'convert' AND convert_request_id IS NOT NULL AND settlement_event_id IS NULL
            AND reverses_entry_id IS NULL AND available_at IS NULL)
    ),
    CONSTRAINT owner_earning_entries_request_check CHECK (
        convert_request_id IS NULL OR (convert_request_id <> '' AND length(convert_request_id) <= 64)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS owner_earning_entries_accrue_event_key
    ON owner_earning_entries (settlement_event_id) WHERE kind = 'accrue';
CREATE UNIQUE INDEX IF NOT EXISTS owner_earning_entries_reverse_key
    ON owner_earning_entries (reverses_entry_id) WHERE kind = 'reverse';
CREATE UNIQUE INDEX IF NOT EXISTS owner_earning_entries_convert_key
    ON owner_earning_entries (owner_user_id, convert_request_id) WHERE kind = 'convert';
CREATE INDEX IF NOT EXISTS owner_earning_entries_owner_available_idx
    ON owner_earning_entries (owner_user_id, available_at);
CREATE INDEX IF NOT EXISTS owner_earning_entries_owner_created_idx
    ON owner_earning_entries (owner_user_id, created_at);

DROP TRIGGER IF EXISTS owner_earning_entries_no_update ON owner_earning_entries;
CREATE TRIGGER owner_earning_entries_no_update
    BEFORE UPDATE ON owner_earning_entries
    FOR EACH ROW EXECUTE FUNCTION market_reject_update();
