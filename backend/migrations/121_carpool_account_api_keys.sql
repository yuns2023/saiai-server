CREATE TABLE IF NOT EXISTS claude_oauth_account_api_keys (
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 api_key_id BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
 PRIMARY KEY (account_id, api_key_id)
);
CREATE INDEX IF NOT EXISTS idx_claude_oauth_account_api_keys_key ON claude_oauth_account_api_keys(api_key_id);
