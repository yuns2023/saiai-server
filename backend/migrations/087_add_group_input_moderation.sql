ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS input_moderation_enabled BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS input_moderation_auto_disable_user BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS input_moderation_categories JSONB NOT NULL DEFAULT '["Jailbreak", "PII", "Non-violent Illegal Acts", "Unethical Acts"]'::jsonb;

COMMENT ON COLUMN groups.input_moderation_enabled IS
    'Submit the latest real user text to the asynchronous input moderation worker.';
COMMENT ON COLUMN groups.input_moderation_auto_disable_user IS
    'Disable a non-admin site user when an unsafe result matches this group policy.';
COMMENT ON COLUMN groups.input_moderation_categories IS
    'Unsafe classifier categories that can trigger automatic user disable; empty means any unsafe category.';

CREATE TABLE IF NOT EXISTS input_moderation_events (
    id              BIGSERIAL PRIMARY KEY,
    job_id          UUID NOT NULL UNIQUE,
    request_id      VARCHAR(128),
    user_id         BIGINT REFERENCES users(id) ON DELETE SET NULL,
    api_key_id      BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    group_id        BIGINT REFERENCES groups(id) ON DELETE SET NULL,
    input_hash      CHAR(64) NOT NULL,
    safety          VARCHAR(32) NOT NULL,
    categories      JSONB NOT NULL DEFAULT '[]'::jsonb,
    action          VARCHAR(32) NOT NULL,
    model_version   VARCHAR(128),
    policy_version  VARCHAR(64),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_input_moderation_events_user_created
    ON input_moderation_events(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_input_moderation_events_group_created
    ON input_moderation_events(group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_input_moderation_events_action_created
    ON input_moderation_events(action, created_at DESC);

COMMENT ON TABLE input_moderation_events IS
    'Metadata-only audit events for asynchronous user-input moderation; raw input is never stored.';
