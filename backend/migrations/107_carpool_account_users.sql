CREATE TABLE IF NOT EXISTS claude_oauth_account_users (
 account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 PRIMARY KEY (account_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_claude_oauth_account_users_user ON claude_oauth_account_users(user_id);
