# User visibility of hosted account quotas

This page covers two views. The group-based view below is the classic user
console page **Hosted Accounts** (`/hosted-accounts`). ccyou.dev
(`frontend-aog`) instead has an ownership-based view, 我的托管号 (`/hosted`);
see [Owner view on ccyou.dev](#owner-view-on-ccyoudev-frontend-aog).

A hosted group is an exclusive group granted to its owning user. Administrators
can enable **Allow users to view account quotas** when creating or editing that
group. The setting defaults to false. Making the group public clears the setting.
User authorization continues to use the existing group grants. The account
ownership used by the ccyou.dev owner view (migration 128) plays no part in
this view.

The user **Hosted Accounts** page lists visible groups and shows each account's
display label (`托管号 #<id>`, never the admin account name), availability,
supported quota windows, reset times and sample timestamp.
Quotas describe the upstream account's overall usage, including usage outside
this platform. Platform spending remains on the existing usage pages. Account
percentages are not averaged into a group quota.

## API and authorization

- `GET /api/v1/groups/hosted` returns group ID, name and platform only.
- `GET /api/v1/groups/:id/account-quota?page=1&page_size=20` returns paginated
  account snapshots. Page size is capped at 100.

Both endpoints require the normal JWT user authentication. Each request reads
current user/group state from repositories. A group must be active, exclusive,
have `show_account_quota=true`, and be explicitly granted to the active user.
The existing minimum user level also applies. Viewing does not require an API
key or authorize model traffic; subscription/billing checks on model traffic
remain independent. Hidden, unauthorized and disabled groups return 404 on the
account endpoint. Responses use `Cache-Control: no-store`.

Account responses allowlist `id`, `name`, `platform`, `status`, `updated_at`,
`stale`, and `windows`. `name` is the display label `托管号 #<id>`, because
admin account names can contain seller emails or notes. Windows contain `key`,
`utilization`, `resets_at`, `expired`, `used_cost_usd`,
`estimated_capacity_usd` and `estimate_sampled_at`. Credentials, account extra
fields, email, notes, proxy information, raw errors and scheduling
configuration are excluded.

### Window costs and capacity estimates

The three cost fields are null unless the current configuration has a single
user: the viewer must be the group's only allowed or subscribed user, and the
account must belong to no other group and list no other user or other user's
key in its carpool lists. This checks the configuration at request time only.
The amounts are the account's usage by all users in the window, so usage by a
user who was removed from the group earlier in the window is still included.
Only the `five_hour` and `seven_day` windows get costs, and only when the
window has a usable reset time.

- `used_cost_usd`: platform usage of the account in the current window, all
  users, at standard price (`usage_logs.total_cost`).
- `estimated_capacity_usd`: platform usage up to the sample time divided by
  the sampled utilization. It needs a sample of at least 10% taken inside the
  window and some platform usage before it; otherwise the estimate and
  `estimate_sampled_at` are null. Usage outside the platform is invisible, so
  in practice it is a lower bound. `estimate_sampled_at` is the time of that
  sample.
- A recorded mid-window restart of the upstream window moves the window start.
  After an automatic restart the utilization recorded at the restart is
  subtracted first, and the increase must be at least 10 percentage points. A
  manual restart gives no estimate.

Failures leave the fields null.

## Snapshot semantics

The service only reads repositories. Opening the page, selecting a group,
paging and refreshing never invokes an upstream client, refreshes tokens or
sends a model request.

- Claude OAuth/setup-token: read existing session-window utilization and
  passive weekly snapshots, timestamped by `passive_usage_sampled_at`. Weekly
  windows are sampled whether or not the response has 5h headers. When
  `passive_usage_5h_absent` is true (no 5h window upstream, e.g. Claude Ultra),
  the 5-hour window is omitted instead of showing a stale or derived value.
- Codex OAuth: read existing 5-hour/weekly snapshots, timestamped by
  `codex_usage_updated_at`. Relative reset times require that sample timestamp.
- Other account types, or missing/invalid utilization values: show no quota
  data for that window. Do not infer upstream usage from local costs or
  scheduling status. A valid utilization without a valid sample timestamp is
  still shown, marked stale.

Missing utilization is distinct from a measured zero. Missing reset times stay
unknown. Sample times are not replaced with page load time. Samples at least
15 minutes old, lacking a valid timestamp, or containing elapsed windows are
marked stale. An elapsed window retains its sampled percentage and displays
"awaiting new data" instead of assuming the account has an unused quota.
The timestamp is the account snapshot's last recorded sample time; existing
storage does not provide a separate timestamp for every window.

A fresh full 5-hour or overall weekly window displays quota exhausted when the
account is otherwise available; error, paused, expired, rate-limited and
temporarily unavailable states take precedence. A full
model-specific window does not imply all models are exhausted. Scheduling
errors are represented by generic status labels, never raw error text.

Refresh reloads visible groups and existing account snapshots. The UI clears
old rows before a request and on failures, aborts requests when changing groups
or leaving the page, and ignores late responses. A local clock updates reset
countdowns and freshness without network polling.

## Persistence and validation

Migration `102_add_group_account_quota_visibility.sql` adds the non-null boolean
with a false default. Ent code and Wire constructors are regenerated from the
canonical schemas/providers. The change is additive and does not alter account
selection, upstream request shapes, billing, or client delivery.

Validation covers permission revocation, public/disabled/unauthorized groups,
minimum user level, credential redaction, pagination, missing versus zero data,
expired snapshots, timestamp handling, group-switch races, UI refresh/error
handling and PostgreSQL visibility round trips. Local validation uses mocks and
disposable test databases; no real provider request is needed.

## Owner view on ccyou.dev (frontend-aog)

ccyou.dev does not use the group-based page above. Its user console has
我的托管号 (`/hosted`), based on **account ownership** (migration
`128_account_owner.sql`) instead of group grants and `show_account_quota`.
Users can also submit their own Claude account there and pause/withdraw it;
see [HOSTED_SELF_ONBOARD.md](HOSTED_SELF_ONBOARD.md).

- An administrator assigns an upstream account to the user who brought it
  with `PUT /api/v1/admin/accounts/:id/owner`. An account that already
  belonged to another user can only be cleared or set back to that user (409
  `ACCOUNT_OWNER_TRANSFER_FORBIDDEN`).
- Every usage row snapshots the account's owner when it is written
  (`usage_logs.account_owner_user_id`). Owner views only count rows whose
  snapshot is the viewer, so usage from before an assignment or from another
  owner's period is never shown.
- The navigation entry appears only when the user owns, or has owned, at least
  one account. The overview page (`/dashboard`) then shows a 我的托管号 line
  linking to `/hosted`.

### API

Both endpoints require the normal JWT user authentication, only read
repositories, and send `Cache-Control: no-store`.

- `GET /api/v1/hosted/owned-accounts` returns `{"items": [...]}`: the
  accounts the user owns now (not deleted, at most 200 by account ID), then
  the accounts the user owned in the past (from the ownership history; deleted
  accounts included). The history query reads at most 200 accounts by account
  ID, current ones included, and drops the current ones afterwards, so with
  many accounts some past ones can be missing. There is no paging. Item
  fields:
  - `account_id`, `current`, `display_name` (`托管号 #<id>`), `platform`,
    `type`, `status`, `updated_at`, `stale`, `windows`.
  - For current accounts, status and windows follow the snapshot rules of the
    group-based view above; the window cost and estimate fields are always
    null here. Past accounts have null `type`, `status` and `updated_at` and
    no windows.
  - `last_24h` and `last_7d`: `self_requests`, `self_standard_cost`,
    `self_service_fee`, `others_requests`, `others_standard_cost`. Standard
    cost is `usage_logs.total_cost`, the site's standard price. The service fee
    is what the owner was charged for their own hosted-metered requests
    (`actual_cost` of rows with `metered_cost` set).
- `GET /api/v1/hosted/owned-accounts/:id/usage?start=&end=&limit=&cursor=`
  returns one page of calls served by the account, newest first, as
  `{"items": [...], "next_cursor": …}`:
  - `start` and `end` are RFC 3339 times, a half-open range of at most 31 days
    (400 `INVALID_USAGE_RANGE`). `limit` is 1–100, default 50 (400
    `INVALID_USAGE_LIMIT`). `cursor` is the opaque `next_cursor` of the
    previous page (400 `INVALID_CURSOR`); it is null on the last page.
  - An account the user neither owns nor owned gives 404
    `HOSTED_ACCOUNT_NOT_FOUND`.
  - Row fields: `created_at`, `model`, `input_tokens`, `output_tokens`,
    `cache_creation_tokens`, `cache_read_tokens`, `standard_cost`, `is_self`,
    `api_key_name`, `charged`. `api_key_name` and `charged` are filled only for
    the owner's own calls. Other users' calls show usage only, never their
    identity, key or charge.

### Page

- Current accounts first, then past accounts (history only). Each row shows
  the display label and a 24 h / 7 d table of the owner's own calls (自用)
  against other users' calls (其他用户): request count and standard cost, plus
  服务费 for the owner's own calls. Current accounts also show a status tag
  and the platform and account type; past accounts show the platform when it
  is still known.
- Current accounts also show their limit windows: utilization bar, reset time,
  "awaiting new data" (已过重置时间，等待新数据) for an elapsed window, the
  sample time and a stale tag.
- Selecting an account lists its calls for today, 7 days, 30 days or a custom
  range of up to 31 days, 50 rows at a time. The caller column says 自己 plus
  the key name, or 其他用户. 实扣 (amount charged) is shown only for the
  owner's own calls.
- Known gaps: the page has labels for the windows `five_hour`, `seven_day`,
  `seven_day_sonnet` and `seven_day_opus`, and for the statuses `available`,
  `needs_attention`, `paused`, `expired`, `rate_limited` and
  `temporarily_unavailable`. The backend can also send the window
  `seven_day_fable` and the status `quota_exhausted`; the page shows those raw
  keys.
