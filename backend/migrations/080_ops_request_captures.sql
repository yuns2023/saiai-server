-- Persist opt-in request captures for troubleshooting.
--
-- This table is intentionally separate from ops_error_logs:
--   - ops_error_logs remains the error list / retry source of truth
--   - ops_request_captures can store success and failure traffic samples
--   - request/response bodies are TEXT so JSON object key order is preserved

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

CREATE TABLE IF NOT EXISTS ops_request_captures (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    outcome VARCHAR(16) NOT NULL DEFAULT 'unknown',
    request_id VARCHAR(128),
    client_request_id VARCHAR(128),

    user_id BIGINT,
    api_key_id BIGINT,
    account_id BIGINT,
    group_id BIGINT,
    client_ip inet,

    platform VARCHAR(32),
    model VARCHAR(100),
    request_path VARCHAR(256),
    inbound_endpoint VARCHAR(128),
    upstream_endpoint VARCHAR(128),
    stream BOOLEAN NOT NULL DEFAULT false,
    session_id VARCHAR(128),
    request_payload_hash VARCHAR(128),
    user_agent TEXT,

    status_code INT,
    upstream_status_code INT,
    error_message TEXT,

    inbound_request_headers JSONB,
    inbound_request_body TEXT,
    inbound_request_body_truncated BOOLEAN NOT NULL DEFAULT false,
    inbound_request_body_bytes INT,

    upstream_request_method VARCHAR(16),
    upstream_request_url TEXT,
    upstream_request_headers JSONB,
    upstream_request_body TEXT,
    upstream_request_body_truncated BOOLEAN NOT NULL DEFAULT false,
    upstream_request_body_bytes INT,

    upstream_response_headers JSONB,
    upstream_response_body TEXT,
    upstream_response_body_truncated BOOLEAN NOT NULL DEFAULT false,
    upstream_response_body_bytes INT,

    input_tokens INT,
    output_tokens INT,
    cache_creation_tokens INT,
    cache_read_tokens INT,
    duration_ms BIGINT,
    first_token_ms BIGINT
);

COMMENT ON TABLE ops_request_captures IS
    'Opt-in sanitized request captures for selected API keys. Stores success and failure samples for request-shape debugging.';

CREATE INDEX IF NOT EXISTS idx_ops_request_captures_api_key_time
    ON ops_request_captures (api_key_id, created_at DESC)
    WHERE api_key_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_request_captures_account_time
    ON ops_request_captures (account_id, created_at DESC)
    WHERE account_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_request_captures_request_id
    ON ops_request_captures (request_id)
    WHERE request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_request_captures_client_request_id
    ON ops_request_captures (client_request_id)
    WHERE client_request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_ops_request_captures_session_time
    ON ops_request_captures (session_id, created_at DESC)
    WHERE session_id IS NOT NULL;
