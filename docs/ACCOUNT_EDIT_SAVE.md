# Saving an account from the admin edit page

`PUT /api/v1/admin/accounts/:id` is the admin edit page's save. The page
submits `credentials` and `extra` built from the account as it was when the
page loaded (`{...props.account.credentials}`), so a save could put back values
the server changed meanwhile: an OAuth token rotated by a background refresh
(the old `refresh_token` then fails with `invalid_grant`), usage samples, quota
counters, rate limits, the carpool maintenance schedule.

## What the save does

The change is applied to the account's **current** row under a row lock
(`accountRepository.UpdateLocked`: `SELECT … FOR UPDATE`, apply, write, commit
in one transaction). Writes committed before the lock (billing, rate limits,
samples, refresh) are part of what is saved; later ones wait for the commit.
The scheduler outbox event is written in the same transaction, so a failed
outbox insert now fails the save.

These keys keep their **current** value, whatever the request carries
(present now → kept; absent now → removed even if the request has it; JSON
`null` in the request does not clear them):

- `credentials` of OAuth and setup-token accounts (by the stored type):
  `access_token`, `refresh_token`, `id_token`, `token_type`, `expires_in`,
  `expires_at`, `scope`, `_token_version`, `email`, `chatgpt_account_id`,
  `chatgpt_user_id`, `organization_id`, `plan_type`, `client_id`,
  `project_id`, `tier_id`, `oauth_type`, `drive_storage_limit`,
  `drive_storage_usage`, `drive_tier_updated_at`. API-key, upstream and
  Bedrock secrets are not protected (the page edits them).
- `extra` runtime keys: quota counters (`quota_used`, `quota_daily_used`,
  `quota_daily_start`, `quota_weekly_used`, `quota_weekly_start`),
  `passive_usage_*`, `session_window_utilization`, the Codex usage snapshot
  (`codex_primary_*`, `codex_secondary_*`, `codex_5h_*`, `codex_7d_*` exact
  keys, `codex_usage_updated_at`, `codex_primary_over_secondary_percent`),
  `model_rate_limits`, `privacy_mode`, `antigravity_quota_scopes`,
  `antigravity_credits_overages`, `drive_*`, and the carpool / single-device
  last-maintenance-day keys. Config keys such as `codex_cli_only` or
  `quota_*_limit` follow the request.
- `quota_daily_reset_at` / `quota_weekly_reset_at` are recomputed only when
  that part's reset config (mode, hour, day, timezone) changes; otherwise the
  stored value, which billing advances, is kept.
- The carpool maintenance schedule (`claude_oauth_carpool_next_maintenance_at`
  and friends) is kept unless the carpool config changes.

To replace OAuth tokens, use the refresh / re-authorization endpoints, not
this PUT. Other callers of the same service method (manual refresh, batch
credentials, Gemini tier refresh) write those keys as before.

## Known limits

- Other fields are still taken from the page as submitted: two admins editing
  different fields, or a page left open for hours, can still overwrite each
  other's / the server's newer `status`, `proxy_id`, `priority`, config keys.
  (A per-field change set with conflict checks would be the full fix.)
- `claude_oauth_single_device_admission_limit` is written by both the admin
  and the maintenance worker; the page's value wins.
- Token refresh still replaces the whole credentials column, so a
  `model_mapping` edited during a refresh can be lost.
- CRS sync, bulk update and group binding (`BindGroups`, separate statement)
  are unchanged.
