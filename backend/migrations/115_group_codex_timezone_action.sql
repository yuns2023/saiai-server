-- Per-group action for the Codex timezone guard (migration 114). 'reject'
-- keeps the existing behavior; 'rewrite' forwards the request with the
-- environment_context timezone/date rewritten instead of rejecting it. The
-- action only applies while codex_cn_timezone_guard is enabled.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups ADD COLUMN IF NOT EXISTS codex_timezone_action VARCHAR(16) NOT NULL DEFAULT 'reject';

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'groups_codex_timezone_action_check'
          AND conrelid = 'groups'::regclass
    ) THEN
        ALTER TABLE groups ADD CONSTRAINT groups_codex_timezone_action_check
            CHECK (codex_timezone_action IN ('reject', 'rewrite'));
    END IF;
END $$;
