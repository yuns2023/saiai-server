ALTER TABLE groups ADD COLUMN IF NOT EXISTS input_moderation_min_confidence INTEGER NOT NULL DEFAULT 90 CHECK (input_moderation_min_confidence BETWEEN 1 AND 100);
ALTER TABLE groups ALTER COLUMN input_moderation_action_mode SET DEFAULT 'warn_then_cooldown';
ALTER TABLE groups ALTER COLUMN input_moderation_disable_after_hits SET DEFAULT 3;
-- Existing explicit enforcement settings remain unchanged.
ALTER TABLE input_moderation_events ADD COLUMN IF NOT EXISTS scores JSONB;
ALTER TABLE input_moderation_events ADD COLUMN IF NOT EXISTS min_confidence INTEGER;
CREATE TABLE IF NOT EXISTS input_moderation_usage_links (
 request_id TEXT NOT NULL, source TEXT NOT NULL, turn_number INTEGER NOT NULL,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 usage_request_id TEXT NOT NULL,
 PRIMARY KEY (request_id, source, turn_number, user_id, api_key_id, group_id, usage_request_id)
);
CREATE INDEX IF NOT EXISTS idx_moderation_usage_lookup ON input_moderation_usage_links(usage_request_id, user_id, api_key_id, group_id);
CREATE INDEX IF NOT EXISTS idx_moderation_event_lookup ON input_moderation_events(request_id, user_id, api_key_id, group_id, source, turn_number);
