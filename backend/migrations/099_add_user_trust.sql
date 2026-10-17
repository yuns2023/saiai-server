-- Administrator-managed eligibility for trusted-only carpool accounts.
ALTER TABLE users ADD COLUMN IF NOT EXISTS is_trusted BOOLEAN NOT NULL DEFAULT FALSE;
