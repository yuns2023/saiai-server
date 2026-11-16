-- Hosted account ownership (P1): the platform user who brought an upstream
-- account. Admin-assigned; owners get a read-only view of their account's usage.
--
-- accounts.owner_user_id: current owner (NULL = platform-owned). ON DELETE SET
-- NULL so a physical user delete never blocks; history lives in the snapshot
-- column below and in account_owner_events.
--
-- usage_logs.account_owner_user_id: snapshot of accounts.owner_user_id as seen
-- by the usage INSERT statement (the repository reads it from accounts inside
-- the INSERT; it is never taken from cached account objects). NULL = platform
-- account or rows written before this migration. No backfill: setting an owner
-- today must not hand over earlier usage.
--
-- account_owner_events: append-only ownership history. The owner columns carry
-- no foreign keys on purpose, so the "an account that belonged to user A can
-- only be cleared or set back to A" rule can never lose its evidence.
--
-- Indexes are built without blocking writes by 129_account_owner_indexes_notx.sql.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS owner_user_id BIGINT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'accounts_owner_user_id_fkey') THEN
        ALTER TABLE accounts ADD CONSTRAINT accounts_owner_user_id_fkey
            FOREIGN KEY (owner_user_id) REFERENCES users(id) ON DELETE SET NULL;
    END IF;
END $$;

ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS account_owner_user_id BIGINT;

CREATE TABLE IF NOT EXISTS account_owner_events (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL,
    old_owner_user_id BIGINT,
    new_owner_user_id BIGINT,
    admin_user_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS account_owner_events_account_idx
    ON account_owner_events (account_id, id);
