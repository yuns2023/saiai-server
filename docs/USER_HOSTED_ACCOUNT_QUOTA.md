# User visibility of hosted account quotas

A hosted group is an exclusive group granted to its owning user. Administrators
can enable **Allow users to view account quotas** when creating or editing that
group. The setting defaults to false. Making the group public clears the setting.
User authorization continues to use the existing group grants; no account-owner
relationship is added.

The user **Hosted Accounts** page lists visible groups and shows each account's
name, availability, supported quota windows, reset times and sample timestamp.
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
`stale`, and `windows`. Windows contain `key`, `utilization`, `resets_at`, and
`expired`. Credentials, account extra fields, email, notes, proxy information,
raw errors and scheduling configuration are excluded.

## Snapshot semantics

The service only reads repositories. Opening the page, selecting a group,
paging and refreshing never invokes an upstream client, refreshes tokens or
sends a model request.

- Claude OAuth/setup-token: read existing session-window utilization and
  passive weekly snapshots, timestamped by `passive_usage_sampled_at`.
- Codex OAuth: read existing 5-hour/weekly snapshots, timestamped by
  `codex_usage_updated_at`. Relative reset times require that sample timestamp.
- Other account types, or missing/invalid samples: show no quota data. Do not
  infer upstream usage from local costs or scheduling status.

Missing utilization is distinct from a measured zero. Missing reset times stay
unknown. Sample times are not replaced with page load time. Samples at least
15 minutes old, lacking a valid timestamp, or containing elapsed windows are
marked stale. An elapsed window retains its sampled percentage and displays
"awaiting new data" instead of assuming the account has an unused quota.
The timestamp is the account snapshot's last recorded sample time; existing
storage does not provide a separate timestamp for every window.

A fresh full 5-hour or overall weekly window displays quota exhausted. A full
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
