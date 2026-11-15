-- User-generated invitation codes and who invited whom.
--
-- redeem_codes.owner_user_id: the user who generated an invitation code from
--   the user console. NULL for every code an admin generated (all existing
--   rows), so existing codes keep working exactly as before.
-- users.invited_by_user_id: copied from the code's owner inside the same
--   transaction that creates the user and consumes the code. Stored on the
--   user because redeem codes are hard-deleted; deleting a used code must not
--   lose the relationship. NULL for admin codes and for everyone who signed up
--   before this migration.
--
-- Both columns are nullable without a default: metadata-only on PostgreSQL.
-- No foreign keys, matching redeem_codes.used_by (users are soft-deleted).

SET LOCAL lock_timeout = '5s';

ALTER TABLE redeem_codes ADD COLUMN IF NOT EXISTS owner_user_id BIGINT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS invited_by_user_id BIGINT;
