-- Support admin risk counters and detection history without indexing non-alerts.
CREATE INDEX IF NOT EXISTS idx_input_moderation_flagged_user_time
 ON input_moderation_results(user_id, updated_at DESC) WHERE status='flagged';
CREATE INDEX IF NOT EXISTS idx_input_moderation_flagged_time
 ON input_moderation_results(updated_at DESC) WHERE status='flagged';
