-- SAIAI client proof v1 (observe mode), see docs/CLIENT_PROOF_V1.md.
--
-- 1. client_devices: one row per enrolled local-proxy device key per user.
--    first_api_key_id is informational only; the device belongs to the user.
-- 2. usage_logs.client_attestation: nullable SMALLINT status, NULL = not
--    collected. No default, no backfill, no index: adding the column is a
--    metadata-only change, but it briefly takes an ACCESS EXCLUSIVE lock, so
--    lock_timeout makes the migration fail fast instead of queueing behind
--    long-running usage_logs queries. The admin 7-day aggregate uses the
--    existing idx_usage_logs_user_created (user_id, created_at).
-- 3. groups.client_attestation_policy: 'off' (default) | 'observe' |
--    'enforce'. 'enforce' is reserved; the v1 admin API accepts off/observe.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS client_devices (
    id                BIGSERIAL PRIMARY KEY,
    device_id         VARCHAR(32) NOT NULL,
    user_id           BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    first_api_key_id  BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    public_key        BYTEA NOT NULL,
    public_key_sha256 CHAR(64) NOT NULL,
    client_version    VARCHAR(64) NOT NULL DEFAULT '',
    os                VARCHAR(32) NOT NULL DEFAULT '',
    arch              VARCHAR(32) NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at      TIMESTAMPTZ,
    last_ip           VARCHAR(64),
    last_claude_at    TIMESTAMPTZ,
    last_codex_at     TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ,
    revoked_by        BIGINT,
    CONSTRAINT client_devices_device_id_key UNIQUE (device_id),
    CONSTRAINT client_devices_user_public_key_key UNIQUE (user_id, public_key_sha256)
);

CREATE INDEX IF NOT EXISTS idx_client_devices_user_active
    ON client_devices(user_id)
    WHERE revoked_at IS NULL;

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_attestation SMALLINT;

ALTER TABLE groups ADD COLUMN IF NOT EXISTS client_attestation_policy VARCHAR(16) NOT NULL DEFAULT 'off';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_client_attestation_policy_check'
          AND conrelid = 'groups'::regclass
    ) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_client_attestation_policy_check
            CHECK (client_attestation_policy IN ('off', 'observe', 'enforce'));
    END IF;
END $$;
