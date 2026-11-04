-- Hosted usage also covers Anthropic groups (Group.UsesHostedUsage since #165).
-- Migration 110's check still required platform = 'openai', so enabling hosted
-- usage on an Anthropic group failed with a constraint violation (HTTP 500).
-- Same rule otherwise; groups is small, so validating it on ADD is cheap.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_hosted_usage_policy;
ALTER TABLE groups ADD CONSTRAINT groups_hosted_usage_policy CHECK (
    NOT hosted_usage_enabled OR
    (platform IN ('openai', 'anthropic') AND is_exclusive AND subscription_type = 'standard' AND rate_multiplier = 0)
);
