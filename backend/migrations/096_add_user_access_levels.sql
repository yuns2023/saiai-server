-- Balance-driven user access levels. Existing group behavior remains unchanged
-- until an administrator assigns groups a required_level_id.
CREATE TABLE IF NOT EXISTS access_levels (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    rank INTEGER NOT NULL CHECK (rank >= 0),
    balance_threshold DECIMAL(20,8) NOT NULL DEFAULT 0 CHECK (balance_threshold >= 0),
    payg_discount_multiplier DECIMAL(10,4) NOT NULL DEFAULT 1
        CHECK (payg_discount_multiplier >= 0 AND payg_discount_multiplier <= 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS access_levels_rank_key ON access_levels(rank);
CREATE INDEX IF NOT EXISTS access_levels_balance_threshold_idx ON access_levels(balance_threshold);

INSERT INTO access_levels (name, rank, balance_threshold, payg_discount_multiplier)
SELECT '默认等级', 0, 0, 1
WHERE NOT EXISTS (SELECT 1 FROM access_levels);

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS auto_level_id BIGINT,
    ADD COLUMN IF NOT EXISTS manual_level_id BIGINT,
    ADD COLUMN IF NOT EXISTS payg_discount_override_enabled BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS required_level_id BIGINT;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_auto_level_id_fkey') THEN
        ALTER TABLE users ADD CONSTRAINT users_auto_level_id_fkey
            FOREIGN KEY (auto_level_id) REFERENCES access_levels(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_manual_level_id_fkey') THEN
        ALTER TABLE users ADD CONSTRAINT users_manual_level_id_fkey
            FOREIGN KEY (manual_level_id) REFERENCES access_levels(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'groups_required_level_id_fkey') THEN
        ALTER TABLE groups ADD CONSTRAINT groups_required_level_id_fkey
            FOREIGN KEY (required_level_id) REFERENCES access_levels(id) ON DELETE RESTRICT;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS users_auto_level_id_idx ON users(auto_level_id);
CREATE INDEX IF NOT EXISTS users_manual_level_id_idx ON users(manual_level_id);
CREATE INDEX IF NOT EXISTS groups_required_level_id_idx ON groups(required_level_id);

-- Preserve previously configured per-user discounts as explicit overrides.
UPDATE users
SET payg_discount_override_enabled = TRUE
WHERE payg_discount_multiplier <> 1;

UPDATE users
SET auto_level_id = (
    SELECT id FROM access_levels ORDER BY rank ASC, id ASC LIMIT 1
)
WHERE auto_level_id IS NULL;

INSERT INTO settings (key, value, updated_at)
VALUES ('access_level_balance_maintenance_enabled', 'false', NOW())
ON CONFLICT (key) DO NOTHING;

CREATE TABLE IF NOT EXISTS user_access_level_events (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    old_level_id BIGINT REFERENCES access_levels(id) ON DELETE SET NULL,
    new_level_id BIGINT REFERENCES access_levels(id) ON DELETE SET NULL,
    source VARCHAR(20) NOT NULL,
    balance_snapshot DECIMAL(20,8) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS user_access_level_events_user_created_idx
    ON user_access_level_events(user_id, created_at DESC);

CREATE OR REPLACE FUNCTION saiai_assign_auto_access_level()
RETURNS TRIGGER AS $$
DECLARE
    maintenance_enabled BOOLEAN := FALSE;
    target_level_id BIGINT;
    target_rank INTEGER;
    current_rank INTEGER;
BEGIN
    SELECT COALESCE(value = 'true', FALSE)
    INTO maintenance_enabled
    FROM settings
    WHERE key = 'access_level_balance_maintenance_enabled';

    SELECT id, rank
    INTO target_level_id, target_rank
    FROM access_levels
    WHERE balance_threshold <= NEW.balance
    ORDER BY rank DESC, id ASC
    LIMIT 1;

    IF target_level_id IS NULL THEN
        SELECT id, rank
        INTO target_level_id, target_rank
        FROM access_levels
        ORDER BY rank ASC, id ASC
        LIMIT 1;
    END IF;

    IF target_level_id IS NULL THEN
        RETURN NEW;
    END IF;

    IF TG_OP = 'INSERT' OR NEW.auto_level_id IS NULL THEN
        NEW.auto_level_id := target_level_id;
        RETURN NEW;
    END IF;

    SELECT rank INTO current_rank FROM access_levels WHERE id = NEW.auto_level_id;
    IF maintenance_enabled OR NEW.balance > OLD.balance THEN
        IF maintenance_enabled OR current_rank IS NULL OR target_rank > current_rank THEN
            NEW.auto_level_id := target_level_id;
        END IF;
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_assign_auto_access_level ON users;
CREATE TRIGGER users_assign_auto_access_level
BEFORE INSERT OR UPDATE OF balance ON users
FOR EACH ROW EXECUTE FUNCTION saiai_assign_auto_access_level();

CREATE OR REPLACE FUNCTION saiai_audit_user_access_level()
RETURNS TRIGGER AS $$
DECLARE
    old_effective BIGINT;
    new_effective BIGINT;
    event_source VARCHAR(20);
BEGIN
    old_effective := COALESCE(OLD.manual_level_id, OLD.auto_level_id);
    new_effective := COALESCE(NEW.manual_level_id, NEW.auto_level_id);

    IF OLD.manual_level_id IS DISTINCT FROM NEW.manual_level_id THEN
        event_source := 'manual';
    ELSE
        event_source := 'auto';
    END IF;

    IF old_effective IS DISTINCT FROM new_effective
       OR OLD.manual_level_id IS DISTINCT FROM NEW.manual_level_id THEN
        INSERT INTO user_access_level_events (
            user_id, old_level_id, new_level_id, source, balance_snapshot
        ) VALUES (
            NEW.id, old_effective, new_effective, event_source, NEW.balance
        );
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS users_audit_access_level ON users;
CREATE TRIGGER users_audit_access_level
AFTER UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION saiai_audit_user_access_level();
