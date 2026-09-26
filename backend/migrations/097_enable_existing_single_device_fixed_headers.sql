-- Preserve the pre-switch behavior of existing single_device accounts.
-- New accounts without an explicit switch remain disabled.
UPDATE accounts
SET extra = jsonb_set(extra, '{claude_oauth_fixed_headers_enabled}', 'true'::jsonb, true)
WHERE platform = 'anthropic'
  AND type IN ('oauth', 'setup-token')
  AND lower(btrim(extra->>'claude_oauth_mode')) = 'single_device'
  AND nullif(btrim(extra->>'claude_oauth_fixed_headers_text'), '') IS NOT NULL
  AND NOT (extra ? 'claude_oauth_fixed_headers_enabled');
