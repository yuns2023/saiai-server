-- accounts.error_at: when the account entered status='error'. Maintained by a
-- trigger so every writer (SetError, admin full update, bulk update, raw SQL)
-- stays consistent without the application ever writing the column.
-- Repeated errors keep the first timestamp; leaving 'error' clears it.
-- Accounts already in 'error' before this migration keep NULL (time unknown).
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE accounts ADD COLUMN IF NOT EXISTS error_at TIMESTAMPTZ;

CREATE OR REPLACE FUNCTION saiai_track_account_error_at()
RETURNS TRIGGER AS $$
BEGIN
    IF NEW.status = 'error' THEN
        IF TG_OP = 'UPDATE' AND OLD.status = 'error' THEN
            NEW.error_at := OLD.error_at;
        ELSE
            NEW.error_at := NOW();
        END IF;
    ELSE
        NEW.error_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS accounts_track_error_at ON accounts;
CREATE TRIGGER accounts_track_error_at
BEFORE INSERT OR UPDATE OF status, error_at ON accounts
FOR EACH ROW EXECUTE FUNCTION saiai_track_account_error_at();
