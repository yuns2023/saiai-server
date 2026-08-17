ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS input_moderation_action_mode VARCHAR(32) NOT NULL DEFAULT 'cooldown_then_disable',
    ADD COLUMN IF NOT EXISTS input_moderation_cooldown_minutes INT NOT NULL DEFAULT 30,
    ADD COLUMN IF NOT EXISTS input_moderation_disable_after_hits INT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS input_moderation_strike_window_hours INT NOT NULL DEFAULT 24,
    ADD COLUMN IF NOT EXISTS input_moderation_dedupe_minutes INT NOT NULL DEFAULT 5;

ALTER TABLE input_moderation_events
    ADD COLUMN IF NOT EXISTS counted_as_strike BOOLEAN NOT NULL DEFAULT false;

CREATE TABLE IF NOT EXISTS user_input_risk_states (
    user_id                  BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    strike_count             INT NOT NULL DEFAULT 0,
    strike_window_started_at TIMESTAMPTZ,
    blocked_until            TIMESTAMPTZ,
    last_event_id            BIGINT REFERENCES input_moderation_events(id) ON DELETE SET NULL,
    last_incident_at         TIMESTAMPTZ,
    reset_at                 TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_input_risk_states_blocked_until
    ON user_input_risk_states(blocked_until)
    WHERE blocked_until IS NOT NULL;

COMMENT ON TABLE user_input_risk_states IS
    'Canonical user-global escalation state for asynchronous input moderation.';
