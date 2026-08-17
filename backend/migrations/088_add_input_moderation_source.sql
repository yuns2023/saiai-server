ALTER TABLE input_moderation_events
    ADD COLUMN IF NOT EXISTS source VARCHAR(32) NOT NULL DEFAULT 'anthropic_messages',
    ADD COLUMN IF NOT EXISTS turn_number INT;

CREATE INDEX IF NOT EXISTS idx_input_moderation_events_source_created
    ON input_moderation_events(source, created_at DESC);

COMMENT ON COLUMN input_moderation_events.source IS
    'Ingress protocol source, for example anthropic_messages, openai_responses_http, or openai_responses_ws.';
COMMENT ON COLUMN input_moderation_events.turn_number IS
    'One-based logical client turn for persistent transports; NULL for sources without a turn.';
