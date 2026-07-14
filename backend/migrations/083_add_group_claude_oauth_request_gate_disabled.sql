ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS claude_oauth_request_gate_disabled BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN groups.claude_oauth_request_gate_disabled IS 'Disable strict Claude OAuth request-shape ingress gate for this group; default false keeps protection enabled.';
