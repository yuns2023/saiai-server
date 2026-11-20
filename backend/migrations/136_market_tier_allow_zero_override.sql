-- Sharing market: allow a user rate override of 0 on a market tier.
--
-- Migration 134 refused overrides <= 0 on a tier (market_tier_group_guard on
-- promotion, market_tier_rate_guard on every override write). A zero
-- override is wanted for free self-use (the admin's own key on a tier). It
-- is safe: the gateway prices every buyer candidate before dispatch and
-- again in the final admission snapshot (which reads the buyer's override);
-- a zero price never selects a listed account, so the request goes to a
-- platform account and no owner earning is written.
--
-- Both functions are replaced with the same body as in 134 except that only
-- negative / NaN overrides are refused (constraint name unchanged, so the
-- server's error mapping is unchanged). CREATE OR REPLACE keeps the function
-- OIDs: the existing triggers use the new bodies, no trigger or table is
-- touched, no table lock or scan. The previous binary works unchanged
-- against this schema (it only reads overrides; its own save-time check is
-- stricter).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE OR REPLACE FUNCTION market_tier_group_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    target_tier BOOLEAN;
    is_insert BOOLEAN := (TG_OP = 'INSERT');
BEGIN
    IF NEW.deleted_at IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- Only changed values are checked (a whole-row save of an unrelated
    -- field takes no extra lock).
    IF NEW.fallback_group_id IS NOT NULL
       AND (is_insert OR NEW.fallback_group_id IS DISTINCT FROM OLD.fallback_group_id) THEN
        SELECT market_tier INTO target_tier FROM groups
         WHERE id = NEW.fallback_group_id AND id <> NEW.id FOR SHARE;
        IF COALESCE(target_tier, FALSE) THEN
            RAISE EXCEPTION 'fallback group % is a market tier', NEW.fallback_group_id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_fallback';
        END IF;
    END IF;
    IF NEW.fallback_group_id_on_invalid_request IS NOT NULL
       AND (is_insert OR NEW.fallback_group_id_on_invalid_request IS DISTINCT FROM OLD.fallback_group_id_on_invalid_request) THEN
        SELECT market_tier INTO target_tier FROM groups
         WHERE id = NEW.fallback_group_id_on_invalid_request AND id <> NEW.id FOR SHARE;
        IF COALESCE(target_tier, FALSE) THEN
            RAISE EXCEPTION 'invalid-request fallback group % is a market tier', NEW.fallback_group_id_on_invalid_request
                USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_fallback';
        END IF;
    END IF;
    IF NEW.market_tier THEN
        -- No lock needed: a tier never has its own claude_code_only
        -- fallback or an invalid-request fallback to itself.
        IF NEW.fallback_group_id IS NOT NULL OR NEW.fallback_group_id_on_invalid_request = NEW.id THEN
            RAISE EXCEPTION 'market tier % is part of a fallback chain', NEW.id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_fallback';
        END IF;
        -- Promotion only: inbound references and stored overrides (writers
        -- of those share-lock this row, so they serialize with the UPDATE).
        IF is_insert OR NOT OLD.market_tier THEN
            IF EXISTS (
                SELECT 1 FROM groups g
                 WHERE g.deleted_at IS NULL AND g.id <> NEW.id
                   AND (g.fallback_group_id = NEW.id OR g.fallback_group_id_on_invalid_request = NEW.id)
            ) THEN
                RAISE EXCEPTION 'market tier % is part of a fallback chain', NEW.id
                    USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_fallback';
            END IF;
            IF EXISTS (
                SELECT 1 FROM user_group_rate_multipliers r
                 WHERE r.group_id = NEW.id AND (r.rate_multiplier < 0 OR r.rate_multiplier = 'NaN')
            ) THEN
                RAISE EXCEPTION 'market tier % has a negative user rate override', NEW.id
                    USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_rate_override';
            END IF;
        END IF;
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION market_tier_rate_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    tier BOOLEAN;
BEGIN
    -- A rate >= 0 is valid everywhere (0 on a tier = free self-use): no lock taken.
    IF NEW.rate_multiplier >= 0 AND NEW.rate_multiplier <> 'NaN' THEN
        RETURN NEW;
    END IF;
    SELECT market_tier INTO tier FROM groups WHERE id = NEW.group_id FOR SHARE;
    IF COALESCE(tier, FALSE) AND NOT (NEW.rate_multiplier >= 0 AND NEW.rate_multiplier <> 'NaN') THEN
        RAISE EXCEPTION 'user rate override on market tier % must not be negative', NEW.group_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'market_tier_rate_override';
    END IF;
    RETURN NEW;
END $$;
