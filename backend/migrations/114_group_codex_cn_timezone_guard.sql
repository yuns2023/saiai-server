-- Opt-in per-group Codex timezone guard. Default off: existing groups keep
-- their current behavior until an administrator enables it.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS codex_cn_timezone_guard BOOLEAN NOT NULL DEFAULT FALSE;
