-- Hosted groups may charge a service fee: the group rate multiplier is the
-- fee as a multiple of the standard price (Group.HostedFeeRate). Migration
-- 119 required rate_multiplier = 0; a zero rate keeps the free policy.
-- Same rule otherwise; groups is small, so validating it on ADD is cheap.
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE groups DROP CONSTRAINT IF EXISTS groups_hosted_usage_policy;
ALTER TABLE groups ADD CONSTRAINT groups_hosted_usage_policy CHECK (
    NOT hosted_usage_enabled OR
    (platform IN ('openai', 'anthropic') AND is_exclusive AND subscription_type = 'standard' AND rate_multiplier >= 0)
);
