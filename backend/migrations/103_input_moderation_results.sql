-- Metadata only: never persist prompts or provider error bodies here.
CREATE TABLE input_moderation_results (
 request_id VARCHAR(128) NOT NULL,
 source VARCHAR(64) NOT NULL,
 turn_number INTEGER NOT NULL DEFAULT 0,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 status VARCHAR(16) NOT NULL CHECK (status IN ('pending','passed','flagged','skipped','failed')),
 reason VARCHAR(64) NOT NULL DEFAULT '',
 scores JSONB,
 min_confidence INTEGER NOT NULL DEFAULT 0,
 model_version VARCHAR(128) NOT NULL DEFAULT '',
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 PRIMARY KEY (request_id,source,turn_number,user_id,api_key_id,group_id)
);
CREATE INDEX idx_input_moderation_results_updated ON input_moderation_results(updated_at);
