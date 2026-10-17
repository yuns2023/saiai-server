ALTER TABLE groups ADD COLUMN IF NOT EXISTS input_moderation_min_confidence INTEGER NOT NULL DEFAULT 90 CHECK (input_moderation_min_confidence BETWEEN 1 AND 100);
ALTER TABLE groups ALTER COLUMN input_moderation_action_mode SET DEFAULT 'warn_then_cooldown';
ALTER TABLE groups ALTER COLUMN input_moderation_disable_after_hits SET DEFAULT 3;
-- Existing explicit enforcement settings remain unchanged.
ALTER TABLE input_moderation_events ADD COLUMN IF NOT EXISTS scores JSONB;
ALTER TABLE input_moderation_events ADD COLUMN IF NOT EXISTS min_confidence INTEGER;
-- Correlation is persisted and retained atomically with the usage row.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS moderation_ref JSONB;
CREATE INDEX IF NOT EXISTS idx_moderation_event_lookup ON input_moderation_events(request_id, user_id, api_key_id, group_id, source, turn_number);
