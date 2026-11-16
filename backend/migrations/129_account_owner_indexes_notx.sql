CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_accounts_owner_user_id
    ON accounts (owner_user_id)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_usage_logs_account_owner
    ON usage_logs (account_owner_user_id, account_id, created_at DESC, id DESC)
    WHERE account_owner_user_id IS NOT NULL;
