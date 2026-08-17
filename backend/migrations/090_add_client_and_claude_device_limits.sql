ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS codex_client_policy VARCHAR(32) NOT NULL DEFAULT 'off',
    ADD COLUMN IF NOT EXISTS claude_device_limit_mode VARCHAR(16) NOT NULL DEFAULT 'off',
    ADD COLUMN IF NOT EXISTS claude_device_base_limit INT NOT NULL DEFAULT 1;

CREATE TABLE IF NOT EXISTS user_group_claude_device_quotas (
    user_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id      BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    bonus_devices INT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS claude_user_devices (
    id               BIGSERIAL PRIMARY KEY,
    user_id          BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id         BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    device_hash      CHAR(64) NOT NULL,
    first_api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    first_seen_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    revoked_at       TIMESTAMPTZ,
    UNIQUE (user_id, group_id, device_hash)
);

CREATE INDEX IF NOT EXISTS idx_claude_user_devices_active
    ON claude_user_devices(user_id, group_id, last_seen_at DESC)
    WHERE revoked_at IS NULL;
