-- Per-group Claude entrypoint allowlist (docs/CLAUDE_ENTRYPOINT_POLICY.md).
-- 'off' (default) skips the check entirely; 'observe' records what /v1/messages
-- clients declare without rejecting; 'enforce' rejects requests whose
-- User-Agent / billing-header cc_entrypoint is not allowed with HTTP 400.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS claude_entrypoint_policy VARCHAR(16) NOT NULL DEFAULT 'off';
ALTER TABLE groups ADD COLUMN IF NOT EXISTS claude_entrypoint_allowlist JSONB NOT NULL DEFAULT '["cli","claude-vscode","sdk-cli"]'::jsonb;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_claude_entrypoint_policy_check'
          AND conrelid = 'groups'::regclass
    ) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_claude_entrypoint_policy_check
            CHECK (claude_entrypoint_policy IN ('off', 'observe', 'enforce'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_claude_entrypoint_allowlist_array_check'
          AND conrelid = 'groups'::regclass
    ) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_claude_entrypoint_allowlist_array_check
            CHECK (jsonb_typeof(claude_entrypoint_allowlist) = 'array');
    END IF;
END $$;

-- Daily (UTC) aggregate of evaluated requests for groups whose policy is
-- observe/enforce. Written in batches by the gateway; rows older than 90 days
-- are deleted by the same writer. No foreign key: a deleted group must not
-- make a whole batch fail.
CREATE TABLE IF NOT EXISTS group_claude_entrypoint_observations (
    group_id          BIGINT       NOT NULL,
    day               DATE         NOT NULL,
    ua_entrypoint     VARCHAR(64)  NOT NULL,
    body_entrypoint   VARCHAR(64)  NOT NULL,
    body_status       VARCHAR(16)  NOT NULL,
    cli_version       VARCHAR(32)  NOT NULL,
    policy            VARCHAR(16)  NOT NULL,
    result            VARCHAR(24)  NOT NULL,
    reason            VARCHAR(24)  NOT NULL,
    requests          BIGINT       NOT NULL,
    first_seen_at     TIMESTAMPTZ  NOT NULL,
    last_seen_at      TIMESTAMPTZ  NOT NULL,
    sample_user_agent VARCHAR(256) NOT NULL DEFAULT '',
    PRIMARY KEY (group_id, day, ua_entrypoint, body_entrypoint, body_status, cli_version, policy, result, reason)
);

CREATE INDEX IF NOT EXISTS idx_group_claude_entrypoint_observations_day
    ON group_claude_entrypoint_observations (day);

-- Per-request warning shown in the admin usage list: set only for requests an
-- observe group found non-compliant but still served. Metadata-only: nullable,
-- no default, no backfill, no index.
-- Last statement on purpose (as in 116): the brief ACCESS EXCLUSIVE lock on
-- usage_logs is taken only after the groups/observation work, and is held for
-- the shortest possible time before COMMIT (lock_timeout above still applies).
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_entrypoint JSONB;
