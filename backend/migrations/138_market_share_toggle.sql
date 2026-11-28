-- Sharing market: 共享 / 自用 as an owner toggle (P3a revision).
--
-- An owned account is either shared (sold in one market tier, buyers only)
-- or self-use (not on the market). The admin no longer allows tiers per
-- account or sets reservations: the account's plan (套餐, e.g. "max")
-- decides which tiers it may sell in, and reservations are always 0.
--
-- groups.market_plan: the plan slug of a market tier. A tier without a plan
-- is not sellable by any account. Only meaningful on market tiers; the
-- server enforces that (no CHECK tying it to market_tier, so the previous
-- binary can still demote a tier without knowing the column).
--
-- account_market_listings.plan: the account's plan, set by the admin (self-
-- onboard approval or the admin sharing settings). The allowed tiers are
-- derived from it on every read and write; allowed_group_ids is no longer an
-- authorization source: it is normalized here (and written from now on) as
-- a mirror of the selected tier, which keeps the listed CHECK satisfied.
-- account_market_listing_events.plan keeps the history complete.
--
-- Reservations: the server ignores reserved_* (selection and final
-- admission treat them as 0). Existing rows are normalized to 0 and the
-- column defaults become 0. There is deliberately no CHECK forcing 0, so a
-- rollback to the previous binary stays possible.
--
-- The partial index serves the owner switch cooldown lookup (last owner
-- list / unlist / switch of one account).
--
-- Sizes: groups and account_market_listings hold tens to hundreds of rows,
-- account_market_listing_events a few per listing change. The new columns
-- are nullable without a default (catalog-only); the CHECKs scan the small
-- tables once. The previous binary works against this schema: it never
-- reads the new columns and writes listings without a plan.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS market_plan TEXT;
ALTER TABLE account_market_listings ADD COLUMN IF NOT EXISTS plan TEXT;
ALTER TABLE account_market_listing_events ADD COLUMN IF NOT EXISTS plan TEXT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'groups_market_plan_format_check' AND conrelid = 'groups'::regclass) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_market_plan_format_check
            CHECK (market_plan IS NULL OR market_plan ~ '^[a-z0-9][a-z0-9_-]{0,31}$');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conname = 'account_market_listings_plan_format_check'
                     AND conrelid = 'account_market_listings'::regclass) THEN
        ALTER TABLE account_market_listings ADD CONSTRAINT account_market_listings_plan_format_check
            CHECK (plan IS NULL OR plan ~ '^[a-z0-9][a-z0-9_-]{0,31}$');
    END IF;
END $$;

ALTER TABLE account_market_listings
    ALTER COLUMN reserved_concurrency SET DEFAULT 0,
    ALTER COLUMN reserved_devices SET DEFAULT 0,
    ALTER COLUMN reserved_admissions SET DEFAULT 0;

UPDATE account_market_listings
SET reserved_concurrency = 0, reserved_devices = 0, reserved_admissions = 0
WHERE reserved_concurrency <> 0 OR reserved_devices <> 0 OR reserved_admissions <> 0;

-- allowed_group_ids only mirrors the selection from now on (listed CHECK of
-- 134: listed ⇒ selected ∈ allowed; revoked rows have no selection).
UPDATE account_market_listings
SET allowed_group_ids = CASE WHEN selected_group_id IS NULL THEN '{}'::bigint[] ELSE ARRAY[selected_group_id] END
WHERE allowed_group_ids IS DISTINCT FROM
      (CASE WHEN selected_group_id IS NULL THEN '{}'::bigint[] ELSE ARRAY[selected_group_id] END);

CREATE INDEX IF NOT EXISTS account_market_listing_events_owner_switch_idx
    ON account_market_listing_events (account_id, created_at DESC, id DESC)
    WHERE action IN ('owner_list', 'owner_unlist', 'owner_switch');
