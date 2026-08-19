ALTER TABLE claude_user_devices
    ADD COLUMN IF NOT EXISTS device_id_encrypted TEXT;

COMMENT ON COLUMN claude_user_devices.device_id_encrypted IS
    'AES-256-GCM encrypted original Claude Code device_id for administrator-only display; device_hash remains the admission key.';
