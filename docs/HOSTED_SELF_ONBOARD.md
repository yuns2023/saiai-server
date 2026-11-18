# Hosted self-onboarding (users submit their own Claude account)

A signed-in user can hand their own Claude subscription account to the site
for hosting. The user authorizes a **setup-token** (inference-only) in their
browser, pastes the authorization code back, and an admin approves the
application. On approval the site creates the account, puts it in the
owner's own exclusive hosted group and grants the owner that group. The
owner then uses it through their API keys and pays the hosted service fee
(P2a: the hosted group's `rate_multiplier` × standard price).

The feature is generic for every site and **off by default**. ccyou.dev
(frontend-aog `/hosted`) is the only user UI; the classic console has the
admin review page (托管申请), the settings and the account-list badges.
Sharing an owner's account into public tier groups is **not** part of this
feature (planned with earnings in P3).

Code: Server #224 (backend, migration 133) and #225 (UI), 2026-11-18.
Design: `merry-rolling-ladybug-p2c-design.md` (sections R2/R3 override the
body).

## Rules that never change

- **Never direct egress.** The token exchange and every later upstream call
  of a self-onboarded account go through a proxy. Exchange proxy = the
  user's submitted proxy, else the site's `hosted_self_onboard_exchange_proxy_id`,
  else the submission is refused. Approval requires a proxy (existing proxy,
  or a new proxy created from the applicant's submission). At runtime a
  self-onboarded account whose proxy is missing, deleted or disabled is not
  schedulable and every upstream path refuses to send. Admins cannot clear
  its proxy (single or bulk edit), cannot delete a proxy that a live account
  uses, and cannot change its platform or type.
- **Tokens never leave the server.** Users get only explicit DTOs
  (status, display name, upstream email, `socks5h://host:port`). The
  authorization code is consumed once; credentials and the proxy password
  live in the application only while it is pending (database CHECK).
  The exchange path logs neither request body nor raw network error.
- **One upstream identity, one live account.** `accounts.upstream_account_uuid`
  (Anthropic `account.uuid`) is unique among live accounts of a platform.
  This also applies to admin create/import: a second live anthropic account
  with the same `extra.account_uuid` returns 409
  `UPSTREAM_ACCOUNT_ALREADY_EXISTS`. Accounts imported without a UUID are
  shown as 身份未知 and are outside this guarantee.

## User flow (ccyou.dev `/hosted` → 托管我的 Claude 号)

1. 生成授权链接 — `POST /api/v1/hosted/applications/auth-url`. Session bound
   to the user, single use, 10 minutes; 10 links per user per hour.
2. Log in on claude.ai, authorize, paste the `code#state` string.
3. Display name (≤ 64 characters) and optional proxy (http, https, socks5,
   socks5h; host may be a domain but must resolve to public addresses only;
   username and password both or neither, each ≤ 100 characters / 255 bytes).
4. 提交审核 — `POST /api/v1/hosted/applications`. At most 3 pending
   applications per user and 10 submissions per 24 h (failed ones count).
   The exchange has a 20 s deadline. The token must be inference-only
   (scope `user:inference`, optionally with `user:profile`) and carry an
   upstream account UUID.

The user sees their applications (`GET /api/v1/hosted/applications`) and
can cancel a pending one. Pending applications expire after 30 days.

Owners can **pause / resume / request withdrawal** of an approved account
(`POST /api/v1/hosted/owned-accounts/:id/pause|resume|withdraw`). Pause has
the same effect as an admin turning 可调度 off: no new requests are routed
to it; queued and in-flight requests finish. Withdrawal stops scheduling for
good and asks an admin to delete the account; the owner cannot undo it.

## Admin flow (classic console → 托管申请)

`GET /api/v1/admin/hosted-applications?status=`, then approve or reject.

Approve (`POST …/:id/approve`) runs in one transaction: lock the application
and the applicant (must still be active), get or create the owner group
(`托管-<user_id>`: anthropic, exclusive, hosted usage on, quota visible,
`rate_multiplier` = `hosted_self_fee_rate`), create the proxy if requested,
create the setup-token account with the carpool template, bind the group,
grant it to the owner, record the owner event, wipe the application's
secrets. Re-approving returns the stored result. A reused owner group must
still be an anthropic hosted group with no other owner's account and no
other user granted.

Reject (`POST …/:id/reject {reason}`) — the reason is shown to the user.

After approval, check the account with the normal 测试账号 button.

## Settings (系统设置 → 托管自助上号)

| Setting | Default | Allowed |
|---|---|---|
| `hosted_self_onboard_enabled` | `false` | boolean |
| `hosted_self_onboard_exchange_proxy_id` | 0 (none) | existing proxy id |
| `hosted_self_fee_rate` | 0.10 | 0–10, ≤ 4 decimals; only for newly created owner groups |
| `hosted_self_onboard_default_concurrency` | 3 | 1–100 |
| `hosted_self_onboard_default_priority` | 1 | 0–10000 |
| `hosted_self_onboard_default_device_limit` | 5 | 1–32 |
| `hosted_self_onboard_default_admissions_per_cycle` | 1 | 0–device limit |
| `hosted_self_onboard_default_interval_hours` | 24 | 1–720 |

Switch **off**: no new links, submissions or approvals. Users still see and
cancel their applications, owners still pause/resume/withdraw, admins still
reject, and the 30-day expiry keeps running.

## Error reasons (`reason` in the error body)

`SELF_ONBOARD_DISABLED`, `_PROXY_REQUIRED`, `_PROXY_INVALID`,
`_PROXY_UNAVAILABLE` (link still usable), `_SESSION_INVALID`,
`_EXCHANGE_FAILED` (carries `metadata.correlation_id` for the server log),
`_IDENTITY_MISSING`, `_ALREADY_HOSTED`, `_PENDING_LIMIT`, `_RATE_LIMITED`,
`_NOT_FOUND`, `_INVALID_STATE`, `_INVALID_INPUT`, `_UNAVAILABLE` (503);
admin account writes: `SELF_ONBOARDED_ACCOUNT_REQUIRES_PROXY`,
`SELF_ONBOARDED_ACCOUNT_TYPE_LOCKED`, `UPSTREAM_ACCOUNT_ALREADY_EXISTS`.

## Deploying migration 133

- Adds `accounts.upstream_account_uuid`, `self_onboarded`,
  `owner_paused_at`, `owner_withdraw_requested_at`, tables
  `hosted_account_applications` and `hosted_owner_groups`, and backfills the
  UUID from anthropic `extra.account_uuid`.
- **It aborts on purpose** if two live anthropic accounts share the same
  normalized `extra.account_uuid`. Before upgrading a site, run read-only:

  ```sql
  SELECT lower(btrim(extra->>'account_uuid')) AS uuid, array_agg(id ORDER BY id)
    FROM accounts
   WHERE platform = 'anthropic' AND deleted_at IS NULL
     AND COALESCE(btrim(extra->>'account_uuid'), '') <> ''
   GROUP BY 1 HAVING count(*) > 1;
  ```

  Any row must be resolved by an admin first.
- The migration holds an exclusive lock on `accounts` for its duration
  (small table; `lock_timeout` 5 s).

## Known limitations (accepted)

- The admin account detail DTO includes credentials (admins already see all
  accounts' tokens).
- A stale scheduler snapshot write (`UpdateLastUsed`) can briefly undo a
  pause in cache until the next refresh; DB candidate lists are correct.
- An admin user edit that saves a stale allowed-groups list can drop a group
  granted by a concurrent approval.
- Proxy rename racing another proxy update can overwrite it; after a proxy
  change the ungrouped candidate bucket is rebuilt at the next full rebuild.
- Admin bulk edit of `extra.account_uuid` does not update the identity
  column.
