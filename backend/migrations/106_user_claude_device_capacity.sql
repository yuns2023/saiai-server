-- User-wide capacity replaces group-scoped admission. Historical registrations and
-- redeemed bonuses stay intact; runtime deduplicates devices and sums user bonuses.
ALTER TABLE users ADD COLUMN IF NOT EXISTS claude_device_base_limit INT NOT NULL DEFAULT 3
    CHECK (claude_device_base_limit BETWEEN 1 AND 100);
CREATE INDEX IF NOT EXISTS idx_claude_user_devices_user_active ON claude_user_devices(user_id, device_hash)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS user_claude_device_quotas (
 user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
 bonus_devices INT NOT NULL DEFAULT 0 CHECK (bonus_devices >= 0),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO user_claude_device_quotas (user_id, bonus_devices)
SELECT user_id, SUM(bonus_devices) FROM user_group_claude_device_quotas GROUP BY user_id ON CONFLICT (user_id) DO NOTHING;
