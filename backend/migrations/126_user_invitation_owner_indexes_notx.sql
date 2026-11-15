CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_redeem_codes_owner_user_id
    ON redeem_codes (owner_user_id, status)
    WHERE owner_user_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_users_invited_by_user_id
    ON users (invited_by_user_id)
    WHERE invited_by_user_id IS NOT NULL;
