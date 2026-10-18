-- NULL preserves the existing four-category detection and enforcement policy.
ALTER TABLE groups ADD COLUMN input_moderation_policy JSONB;
ALTER TABLE input_moderation_results ADD COLUMN decision_snapshot JSONB;
ALTER TABLE input_moderation_events ADD COLUMN decision_snapshot JSONB;
