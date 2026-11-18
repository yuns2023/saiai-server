-- Hosted self-onboarding (P2c): users submit their own Claude setup-token
-- account for hosting; an admin approves it.
--
-- accounts.upstream_account_uuid: normalized upstream identity (Anthropic
-- account.uuid). Live rows of one platform may not share it (partial unique
-- index below). Backfilled only from anthropic rows whose extra.account_uuid
-- is a canonical UUID; empty/invalid values stay NULL ("identity unknown").
-- Two live anthropic rows with the same normalized UUID make this migration
-- FAIL on purpose: an admin must resolve the duplicate first (no auto-merge).
--
-- accounts.self_onboarded: created from an approved application; such
-- accounts must always use a proxy (never direct egress).
-- accounts.owner_paused_at / owner_withdraw_requested_at: owner stopped new
-- scheduling (pause) or asked for removal (withdraw).
--
-- hosted_account_applications: pending submissions hold the upstream
-- credentials until review; every terminal state has them wiped (CHECK).
-- hosted_owner_groups: each owner's auto-created exclusive hosted group.
--
-- accounts is small (hundreds of rows), so the unique index is built inside
-- this transaction; lock_timeout bounds the wait for the table lock.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS upstream_account_uuid UUID;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS self_onboarded BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS owner_paused_at TIMESTAMPTZ;
ALTER TABLE accounts ADD COLUMN IF NOT EXISTS owner_withdraw_requested_at TIMESTAMPTZ;

DO $$
DECLARE
    dup RECORD;
    invalid_ids TEXT;
BEGIN
    SELECT lower(btrim(extra->>'account_uuid', E' \t\r\n')) AS uuid_text,
           string_agg(id::text, ',' ORDER BY id) AS ids
      INTO dup
      FROM accounts
     WHERE platform = 'anthropic'
       AND deleted_at IS NULL
       AND upstream_account_uuid IS NULL
       AND btrim(COALESCE(extra->>'account_uuid', ''), E' \t\r\n') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
       AND lower(btrim(extra->>'account_uuid', E' \t\r\n')) <> '00000000-0000-0000-0000-000000000000'
     GROUP BY lower(btrim(extra->>'account_uuid', E' \t\r\n'))
    HAVING COUNT(*) > 1
     LIMIT 1;
    IF FOUND THEN
        RAISE EXCEPTION 'migration 133: live anthropic accounts % share extra.account_uuid %; resolve the duplicate before upgrading', dup.ids, dup.uuid_text;
    END IF;

    SELECT string_agg(id::text, ',' ORDER BY id)
      INTO invalid_ids
      FROM accounts
     WHERE platform = 'anthropic'
       AND deleted_at IS NULL
       AND type IN ('oauth', 'setup-token')
       AND upstream_account_uuid IS NULL
       AND NOT (
           btrim(COALESCE(extra->>'account_uuid', ''), E' \t\r\n') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
           AND lower(btrim(extra->>'account_uuid', E' \t\r\n')) <> '00000000-0000-0000-0000-000000000000'
       );
    IF invalid_ids IS NOT NULL THEN
        RAISE NOTICE 'migration 133: anthropic oauth/setup-token accounts without a usable account_uuid (identity unknown): %', invalid_ids;
    END IF;
END $$;

UPDATE accounts
   SET upstream_account_uuid = lower(btrim(extra->>'account_uuid', E' \t\r\n'))::uuid
 WHERE platform = 'anthropic'
   AND deleted_at IS NULL
   AND upstream_account_uuid IS NULL
   AND btrim(COALESCE(extra->>'account_uuid', ''), E' \t\r\n') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
   AND lower(btrim(extra->>'account_uuid', E' \t\r\n')) <> '00000000-0000-0000-0000-000000000000';

-- Fails (and with it the migration) if a backfilled UUID collides with one
-- that was already set.
CREATE UNIQUE INDEX IF NOT EXISTS accounts_platform_upstream_account_uuid_live_key
    ON accounts (platform, upstream_account_uuid)
    WHERE deleted_at IS NULL AND upstream_account_uuid IS NOT NULL;

CREATE TABLE IF NOT EXISTS hosted_owner_groups (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    group_id BIGINT NOT NULL UNIQUE REFERENCES groups(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS hosted_account_applications (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    display_name VARCHAR(64) NOT NULL,
    platform VARCHAR(50) NOT NULL DEFAULT 'anthropic',
    upstream_account_uuid UUID NOT NULL,
    upstream_org_uuid VARCHAR(100),
    upstream_email VARCHAR(320),
    credentials JSONB,
    proxy_protocol VARCHAR(16),
    proxy_host VARCHAR(255),
    proxy_port INT,
    proxy_username VARCHAR(255),
    proxy_password VARCHAR(255),
    account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL,
    group_id BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    reviewed_by BIGINT,
    reviewed_at TIMESTAMPTZ,
    reject_reason VARCHAR(500),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT hosted_account_applications_status_check
        CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    -- Credentials live only while pending; every terminal state is wiped.
    CONSTRAINT hosted_account_applications_terminal_wiped
        CHECK (status = 'pending' OR (credentials IS NULL AND proxy_password IS NULL)),
    CONSTRAINT hosted_account_applications_pending_credentials
        CHECK (status <> 'pending' OR credentials IS NOT NULL),
    CONSTRAINT hosted_account_applications_proxy_shape
        CHECK ((proxy_host IS NULL AND proxy_protocol IS NULL AND proxy_port IS NULL)
            OR (proxy_host IS NOT NULL AND proxy_protocol IS NOT NULL AND proxy_port IS NOT NULL
                AND proxy_protocol IN ('http', 'https', 'socks5', 'socks5h')
                AND proxy_port BETWEEN 1 AND 65535))
);

CREATE UNIQUE INDEX IF NOT EXISTS hosted_account_applications_pending_upstream_key
    ON hosted_account_applications (platform, upstream_account_uuid)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS hosted_account_applications_user_idx
    ON hosted_account_applications (user_id, id DESC);
CREATE INDEX IF NOT EXISTS hosted_account_applications_status_idx
    ON hosted_account_applications (status, created_at, id);
