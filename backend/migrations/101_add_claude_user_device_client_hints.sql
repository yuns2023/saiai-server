-- Last non-empty inbound UA and Stainless display headers. Historical rows
-- are populated when their device is next seen; admission identity is unchanged.
ALTER TABLE claude_user_devices
    ADD COLUMN IF NOT EXISTS client_hints JSONB NOT NULL DEFAULT '{}'::jsonb;
