-- Encrypted moderation evidence, its access audit, and cross-turn strike
-- fingerprints.
--
-- Both tables are new and empty. input_moderation_events is tiny (production,
-- last 30 days: 2 rows), so the nullable column without default and the
-- partial index stay in this ordinary transactional migration.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS input_moderation_evidence (
    id          BIGSERIAL PRIMARY KEY,
    event_id    BIGINT NOT NULL REFERENCES input_moderation_events(id) ON DELETE CASCADE,
    check_index INT NOT NULL CHECK (check_index >= 0),
    kinds       JSONB NOT NULL,
    aad         JSONB NOT NULL,
    char_count  INT NOT NULL CHECK (char_count >= 0),
    key_id      VARCHAR(32) NOT NULL,
    ciphertext  TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL,
    hold_until  TIMESTAMPTZ NULL,
    hold_reason TEXT NULL,
    hold_by     BIGINT NULL,
    UNIQUE (event_id, check_index)
);

CREATE INDEX IF NOT EXISTS idx_input_moderation_evidence_expires
    ON input_moderation_evidence(expires_at);

COMMENT ON TABLE input_moderation_evidence IS
    'AES-256-GCM encrypted text of moderation checks that crossed the final threshold. The only persisted store of moderated text besides the encrypted task queue.';
COMMENT ON COLUMN input_moderation_evidence.aad IS
    'Immutable authenticated context (job_id, user_id, group_id, check_index, kinds) used to rebuild the GCM additional data.';

-- Access rows outlive evidence and events on purpose (no foreign key).
CREATE TABLE IF NOT EXISTS input_moderation_evidence_access (
    id            BIGSERIAL PRIMARY KEY,
    event_id      BIGINT NOT NULL,
    admin_user_id BIGINT NOT NULL,
    auth_method   VARCHAR(16) NOT NULL,
    action        VARCHAR(16) NOT NULL CHECK (action IN ('view', 'hold', 'release')),
    reason        TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_input_moderation_evidence_access_event
    ON input_moderation_evidence_access(event_id, created_at);
CREATE INDEX IF NOT EXISTS idx_input_moderation_evidence_access_created
    ON input_moderation_evidence_access(created_at);

ALTER TABLE input_moderation_events ADD COLUMN IF NOT EXISTS incident_fingerprint CHAR(64) NULL;

CREATE INDEX IF NOT EXISTS idx_input_moderation_events_user_fingerprint
    ON input_moderation_events(user_id, incident_fingerprint, created_at)
    WHERE incident_fingerprint IS NOT NULL;
