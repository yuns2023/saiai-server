-- Persist the per-user token version that invalidates issued JWTs and
-- refresh tokens. The service has always compared and incremented
-- User.TokenVersion, but the column never existed, so every user stayed at 0
-- and a password change did not revoke existing sessions.
--
-- Every token issued so far carries version 0, so the default keeps them
-- valid: deploying this does not log anyone out. A constant default is a
-- metadata-only change on PostgreSQL 11+.

SET LOCAL lock_timeout = '5s';

ALTER TABLE users ADD COLUMN IF NOT EXISTS token_version BIGINT NOT NULL DEFAULT 0;
