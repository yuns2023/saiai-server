# User API Key usage analytics

## Admin high-cost request filter

`GET /api/v1/admin/usage` accepts optional `total_cost_gt`, a positive USD
amount up to 1,000,000. It returns individual usage records whose original
`total_cost` is strictly greater than that amount, before group multipliers or
user discounts. The admin UI starts with a $2 threshold that can be changed
or turned off. The filter applies to the record list and its CSV export;
summary cards and charts continue to cover the selected time range.

The user dashboard treats an API Key as a general reporting dimension. It does
not create or expose employee, member, or cost-center entities. A customer can
choose any naming convention for Key names.

## Dashboard filters

All usage surfaces use the same Token presentation. Total Tokens includes
uncached input, output, cache read and cache write; cache writes with 5-minute
and 1-hour lifetimes are subcategories of cache write and are not added twice.
Summary cards display all four categories, including zeros. Recent-record totals
also include both cache categories. Native Chat turn records without measured
Token usage continue to show an unavailable value rather than an invented zero.

Compact Token values use decimal K/M/B with at most two decimal places and no
trailing zeros. Card and table counts provide exact comma-separated integers
on hover, keyboard focus or tap. Chart axes use the same compact formatter;
chart tooltips use exact integers. Compact values are rounded and are not used
to reconcile sums. The server's integer aggregates remain authoritative.

User usage statistics now expose `total_cache_creation_tokens` and
`total_cache_read_tokens` alongside the compatible `total_cache_tokens` sum.
The public Key lookup's `usage.range` likewise exposes `cache_creation_tokens`
and `cache_read_tokens` while retaining `cache_tokens`. These are additive
reporting fields from existing usage records; no schema or billing change is
required.

These authenticated user endpoints accept an optional owned `api_key_id`:

- `GET /api/v1/usage/dashboard/stats`
- `GET /api/v1/usage/dashboard/trend`
- `GET /api/v1/usage/dashboard/models`

The server verifies Key ownership before running an aggregate query. A selected
Key filters the summary, trend, and model data. The dashboard also applies the
same Key and date range to its recent-usage request.

## Actual group filter

The user usage records page and the user dashboard can filter by the final
(actual) group of each usage record: `usage_logs.group_id` after smart switch
or fallback, shown as the actual group column. These authenticated user
endpoints accept an optional positive `group_id`:

- `GET /api/v1/usage` and `GET /api/v1/usage/stats`;
- `GET /api/v1/usage/dashboard/stats`, `/trend` and `/models`; and
- `GET /api/v1/usage/dashboard/api-key-breakdown`.

A missing value means all groups; zero, negative or non-numeric values return
HTTP 400. Every query is forced to the current user's own records, so a group
ID only narrows those records and needs no group ownership check. It combines
with an owned `api_key_id`. In the Key ranking the group narrows usage only:
the Key inventory, including zero-usage Keys, is unchanged. With a group
selected, dashboard Key counts stay user-wide unless a Key is also selected,
the summary keeps each view's existing TPM definition, and the server's today
boundary is unchanged.

`GET /api/v1/usage/groups` takes `start_date`, `end_date` and `timezone` and
returns `[{"id", "name"}]` for the groups present in the current user's records
in that range, most used first. It includes groups the user can no longer use
and shows each group's current name; it never returns other users' groups or
group configuration. The Web UI loads the options for the selected range, keeps
a selected group that is absent from a new range (shown as `#<id>`), reads
`?group_id=` on the usage records page, passes it from the dashboard's details
link, and applies it to CSV exports, whose filters are frozen when the export
starts.

## Cache read analytics

Admin and user summary cards, Key breakdown rows, and the public Key usage
summary show the same cache read rate:

`cache_read_tokens / (input_tokens + cache_read_tokens + cache_creation_tokens)`

Stored input tokens are already exclusive of cache. Output is excluded, and
cache write lifetime subcategories must not be added again. Rates are computed
from full integer counts before formatting to two percentage decimals. Missing,
invalid, or zero-denominator data shows `—`; measured input with zero reads
shows `0.00%`. This is a token-weighted rate, not a request hit rate.

Admin dashboard/usage and user dashboard charts show the rate alongside
uncached input and request volume. The selected-period rate uses summed token
counts, not the average of bucket percentages. Model tables also show rates.
Admin usage filters (model, group, account, user and Key) apply to the cache
chart; users can select only their own Keys through existing ownership checks.

Trend endpoints and admin dashboard snapshot v2 additionally accept
`granularity=15m`. Quarter-hour buckets use raw usage records, preserving the
existing query timezone and filters; hourly and daily aggregate paths remain
unchanged. Prefer a short selected range for quarter-hour inspection. Only
intervals with recorded usage are shown. These charts describe recorded usage,
not requests that failed before a usage record was persisted.

This is an observation feature, with no automatic anomaly classification or
alert threshold. Compare the same model/workload across historical ranges;
first requests, cache warming and workload mix can change the overall rate.
No additional model traffic, billing changes, or database migration is needed.

## Key ranking

`GET /api/v1/usage/dashboard/api-key-breakdown` returns the current user's
non-deleted Key inventory, including zero-usage Keys, with usage aggregated for
the requested range.

Query parameters:

- `start_date` and `end_date`: inclusive calendar dates in `YYYY-MM-DD`;
- `timezone`: IANA timezone supplied by the Web client;
- `page` and `page_size`: pagination, with at most 100 rows per page; and
- `sort`: `actual_cost_desc`, `requests_desc`, `tokens_desc`,
  `last_used_desc`, or `name_asc`.

The response contains Key ID, Key name, status, last-used time, request count,
the four token classes, total tokens, standard cost, actual cost, and actual
cost share. Its summary covers all matching Keys, not only the current page.
The raw API Key value is never selected or returned.

The Web UI supports ranking by cost, requests, tokens, last use, or Key name;
row-level filtering; deep links into detailed usage; and a CSV export.
Historical availability follows the deployment's usage-log retention policy;
this first version does not add a separate long-term per-Key aggregate table.

The older batch Key-usage endpoint retains its compatibility fields. Its
`total_actual_cost` default window is the last 30 days, and the Key-management
UI labels it as such rather than as lifetime usage.

## Administrator Key spending ranking

`GET /api/v1/admin/dashboard/api-keys-ranking` is administrator-only. Without
`user_id` it ranks global usage; a positive `user_id` selects the usage recorded
for that user. It accepts the dashboard's half-open RFC3339 `start_time` /
`end_time` range or inclusive `start_date` / `end_date` calendar range with
`timezone`. Precise timestamps take precedence. `limit` defaults to 12 and is
capped at 50.

Rows are ordered by standard `total_cost` (configured base price before
multipliers), then user-billed `actual_cost`, total tokens, Key ID and user ID
for stable ties. Hosted usage is included: its `actual_cost` is always zero and
its `total_cost` is the recorded reference price, so the standard cost is the
only basis comparable across hosted and billed groups. Each row contains Key
ID/name, user ID/email, request count, `hosted_requests`, standard cost (`cost`),
actual cost, and the four token classes. Totals (`total_cost`,
`total_actual_cost`, requests, tokens) and consumption shares cover all Keys in
the selected scope, before the ranking limit; shares use the standard cost. Historical
usage of soft-deleted Keys is retained; zero-usage Keys are omitted. Raw Key values
are never selected or returned. Results use a 30-second cache separated by time
range, user scope, and limit.

The dashboard user spending ranking (`users-ranking`) uses the same basis and
fields. The dashboard Key/user usage trends pick their Top N by tokens across
hosted and billed usage. Dashboard platform totals, trends and model
distribution still exclude hosted rows and report them in the separate hosted
usage card. Per-user drill-downs from distribution charts order users by
standard cost.

The administrator dashboard provides a global ranking, user search and per-user
ranking, proportional spending bars, and links into Key-filtered usage records
that preserve the selected range. Each row shows the Key's cache read rate as
defined in [Cache read analytics](#cache-read-analytics); a low read rate alone
does not classify abuse.

## Analytics validation notes

- Check global and user scopes separately. Calculate spending shares against all
  matching Keys before Top N. Administrator rankings use standard `cost` /
  `total_cost` (hosted usage included); include a hosted Key in test data.
- Test the destination of a drill-down link: logs, totals and charts must consume
  both `user_id` and `api_key_id` and retain the exact selected time range.
- Reject stale async results after user/range changes. Cache keys must include
  scope, limit and precise range boundaries.
- Use local mock data without real provider traffic. Cover zero reads, writes
  without reads, output-heavy records, absent input and zero-cost scopes.
- In browser checks, mark the mock user's onboarding tour as already seen using
  the storage key derived from the current tour implementation. Do not force
  clicks through an overlay or change production onboarding to make tests pass.
- Inspect screenshots as well as runtime errors and page overflow. Check row
  height and readable column widths on narrow screens; horizontally scroll dense
  tables rather than wrapping identifiers one character per line. Verify both
  light and dark themes.

## Self-service Key lookup

`GET /v1/usage` is the read-only, API-Key-authenticated contract behind the
public `/key-usage` page. Send the Key in `Authorization: Bearer ...` or an
existing supported Key header. Keys in query parameters are rejected. The
endpoint performs no upstream/model request and returns `Cache-Control:
no-store`.

The response keeps the legacy `mode` field for existing clients:

- `quota_limited` means the Key itself has a total quota or a rolling 5-hour,
  daily, or 7-day limit;
- `unrestricted` means the Key has no Key-level quota.

`billing_mode` is independent and is either `subscription` or `balance`. A Key
can therefore be `quota_limited` while also using a subscription. In that
case, the response includes both the Key's `quota` / `rate_limits` and the
shared `subscription` object. Clients must not treat `mode` as the billing
source. New clients should use the structured `billing` and `key_limits`
objects; the legacy top-level fields remain for compatibility.

The wallet disclosure rule is intentionally narrow:

- a balance-billed Key with no total or window spending limit receives its
  current wallet balance;
- once any Key spending limit is configured, the response identifies the
  billing source and whether it is currently available but omits the wallet
  amount; and
- subscription Keys never fall back to a wallet display when an active
  subscription cannot be found.

The database and compatibility API retain the historical `rate_limit_*` field
names. They are monetary spending limits in rolling windows, not request or
token throughput limits. User-facing text calls them “spending window limits.”

Subscription window values are shared by the user's subscription to the bound
group, not reserved for one Key. The object includes 5-hour, daily, weekly,
and monthly limits when configured, effective usage, the minimum currently
available amount, expiry, and reset times. A window that has elapsed is
reported as zero usage without mutating persistence; normal billing traffic
performs the asynchronous window maintenance. `subscription_status` is
`not_found` when the Key is bound to a subscription group but no active
subscription is available.

Usage totals, model aggregation, and recent records remain scoped to the
queried Key. The optional inclusive-calendar `start_date` and `end_date`
parameters apply to all three public range views; they never carry
credentials. `records_page` and `records_page_size` paginate recent records,
with a maximum page size of 50. A recent record exposes only its timestamp,
model, token classes, total tokens, actual cost, duration, and request type. It
does not expose user, account, Key, session, request, IP, or payload identifiers.

## OpenAI service-tier billing

Gateway forwarding and local billing have separate boundaries. Preserve the
request and upstream response shape, but do not let a downstream Gateway's
reported tier reduce the local charge below an explicit request tier.

Normalize the supported tiers as `priority`, `default`, and `flex`, ordered
from most to least expensive. When both request and response tiers are known,
the effective billing tier is the more expensive one:

- an explicit `priority` request remains `priority` even if the downstream
  response reports `default`;
- a response may raise the effective tier above the request tier; and
- an absent value, or a request/response value of `auto`, does not invent a
  tier.

The request `service_tier` is forwarded unchanged, so the Gateway only admits
values it can bill. Accepted request values (case- and space-insensitive) are
`auto`, `default`, `flex`, `priority`, and `fast` (the SAIAI alias of
`priority`); an absent, `null`, or empty value is also accepted. Any other
value, including a non-string or a premium tier such as `ultrafast` or
`scale`, is rejected before an account is selected or an upstream is
contacted: HTTP Responses (including `/responses/compact`) returns `400` with
`type: invalid_request_error`, `param: service_tier`, `code: invalid_value`;
a WebSocket `response.create` (first or later turn, v1 or v2 passthrough) is
rejected like other per-turn validation failures, by closing the client
connection with policy violation (1008) before that frame is sent upstream.
A nested `response.service_tier` is checked too. The handler checks the first
WebSocket message before account selection; afterwards the v1 ingress parser
and the v2 relay check every client frame once (v2 includes frames the relay
does not classify as `response.create`). When the v2 relay rejects a frame
while an earlier turn is still running upstream, the rejected frame is not
forwarded, every in-flight turn is drained for its terminal usage and billed,
and then the client is closed with 1008. v1 reads the next client frame only
after the current turn has completed and been billed (each upstream read is
bounded by `gateway.openai_ws.read_timeout_seconds`).

The v2 drain is the same after a client disconnect, and after a failed write
to the client (the usual way a vanished client surfaces): the relay switches to
discard mode instead of ending the upstream read. While forwarded turns are
outstanding the relay keeps reading the upstream, discarding output, until
every turn has reported terminal usage, bounded by
`gateway.openai_ws.read_timeout_seconds` (default 15 minutes) for the whole
drain and by the same value as the idle watchdog. Only when no turn is
outstanding does the short 1.2-second drain apply. Pipelined v2 turns of one
session share a single user/account concurrency slot pair, which is held until
the last outstanding turn has finished (including turns finished by the
drain); this is intentional, because the upstream account is still generating
for that user. Slots expire in Redis `gateway.concurrency_slot_ttl_minutes`
after acquisition and are not renewed while held (the cache exposes no renewal
to holders); a turn and its drain that outlive the TTL lose the reservation
early, which can over-admit concurrency but does not affect billing. If the cap or an
upstream failure ends the drain first, the relay reports the unfinished
response IDs and an error is logged; their usage is unknown because the relay
learns usage only from terminal events.

Duplicate keys are rejected the same way (HTTP `400` with
`code: duplicate_key` and `param` naming the key; WebSocket close 1008) for
`type`, `model`, `service_tier`, `previous_response_id`, `response` and
`stream`, at the top level and inside a top-level `response` object. The
Gateway reads these with gjson, which returns the first occurrence (sjson
also rewrites only the first), whereas `encoding/json` (the HTTP request map)
and many upstream parsers keep the last. Without the rejection a v2 frame such
as `{"type":"response.cancel","type":"response.create",...}` would be relayed
as a control frame without a billable turn while the upstream could execute
it as a new response.

If an upstream nevertheless reports a tier the resolver does not know, the
turn is billed as `priority`, the most expensive tier with configured prices,
and a warning with the reported value, account, model and upstream request ID
is logged. Under-billing an unknown premium tier is unbounded; billing it as
`priority` is a bounded, visible over-charge. Reported `default`, `flex`,
`priority`/`fast`, `auto` and absent values bill exactly as before.

For OpenAI models without an official Priority/Fast tier (see
[MODEL_PRICING_MANAGEMENT.md](MODEL_PRICING_MANAGEMENT.md)), BillingService
prices an effective `priority` tier at Standard prices unless an administrator
has a Priority price override for the billed model. The usage record keeps
`priority` as its tier; its pricing snapshot sets
`priority_billed_as_standard`.

Apply the same resolver to HTTP, WebSocket, and WebSocket v2 usage recording.
Store the effective tier with the usage record while returning the original
upstream response unchanged.

User and administrator usage tables show the effective billing tier as
`Fast`, `Standard`, or `Flex` beside the charged amount. Historical records are
not recalculated merely because this resolver changes; any backfill is a
separate, explicitly reviewed billing operation.

Administrator usage also shows an amber long-context indicator in the fee
column. This is separate from the service-tier badge and whole-request discount.
Use the recorded calculator decision; label inference for older records, and
never recalculate historical fees from the current price catalogue. This
administrator pricing detail does not expand the public Key analytics payload.
See [MODEL_PRICING_MANAGEMENT.md](MODEL_PRICING_MANAGEMENT.md) for snapshot
precedence, threshold units, tier exclusions and pricing overrides.

## Image request endpoints

Image usage records keep the client-facing endpoint and the actual provider
request path separately. Native Codex image requests use
`/backend-api/codex/images/generations` or `/backend-api/codex/images/edits`.
API-key image requests retain the configured provider base path. The public
Images API compatibility adapter for OAuth accounts records its actual Codex
Responses path, `/backend-api/codex/responses`.

These paths come from the forwarded request, rather than from an inferred
OpenAI default. Query parameters and credentials are not included. This is
usage metadata only; existing records and request/response bodies are unchanged.

The direct Image API is billed by image tokens without service-tier pricing.
JSON generation bodies with any duplicate top-level key, and multipart edit
bodies with a repeated field other than `image`/`image[]`, are rejected with
`400`: the Gateway reads the first value while the API-key path forwards the
original bytes, so for example `"stream":false,"stream":true` could make the
upstream stream while the Gateway expects JSON. `service_tier` is accepted only
when absent, `null`, empty, `auto` or `default`.

A successful direct or native image response is parsed as JSON when it is
valid JSON, otherwise as SSE (multi-line `data:` fields joined; usage from the
last event carrying `usage`; `*.completed` events count as images only when
they carry `b64_json` or `url`). Only entries with image data are counted. If
images were returned without image output usage (including input-only usage)
and the billed model resolves to the official token-priced `gpt-image-*`
family, each such image is charged 6,240 output image tokens (the largest
documented `gpt-image-1` output, high quality 1024x1536). Other models,
including compatible models with their own token prices, keep billing only
the reported usage. In both cases a warning with account, model, resolved
pricing model and upstream request ID is logged. SSE per-image usage on
image-bearing `*.completed` events is summed and the floor applies per image
missing output usage. An event that carries usage without a single image is
treated as an aggregate covering every image: it replaces only the fields it
reports (input-only aggregate usage keeps the per-image output), so nothing is
double counted. Images listed in an aggregate `data` array beyond the
per-image completed events are floored unless an aggregate output covers
them. Other non-standard shapes (conflicting or partial aggregates) are not
modelled; the last aggregate wins. The OAuth Images compatibility adapter applies
the same floor to its returned images.

## Input cache read rate

The administrator usage card shows **Input cache read rate**:
`cache_read / (uncached_input + cache_creation + cache_read) × 100`.
Cache writes count only in the denominator; output tokens do not enter the
ratio. Calculate from unrounded totals and round only the displayed percentage
to one decimal place. Inputs without cache reuse show 0.0%; missing statistics
or a zero input denominator show —. This is a token-weighted input reuse metric,
not the fraction of requests that hit a cache or the share of total tokens used
for cache reads and writes. The total Token card still includes all four classes.

Regression coverage must mount the actual usage card and distinguish cache
writes from reads, vary output independently, cover zero/missing inputs, and
keep the formula hint aligned with both locales.

### Usage billing source breakdown

Usage statistics expose `total_balance_cost`, `total_subscription_cost`, and
`total_unclassified_cost` alongside the unchanged `total_actual_cost` sum.
These buckets aggregate persisted `usage_logs.billing_type` values 0, 1, and
unknown values respectively, using the same filters and time boundaries in one
query. A deleted subscription relation does not reclassify its historical usage.
No database migration or billing calculation change is required.

The usage pages show balance charges separately from subscription quota usage.
Subscription usage is not the subscription purchase price, cash revenue, or the
remaining quota in a reset window. Configured base cost uses recorded usage cost;
it is not a payment total. Charts retaining the combined metric explicitly say
that it includes subscription quota. CSV exports include the billing source.
Missing breakdown fields render as unavailable, not zero, for older API servers.

## Hosted OpenAI reference usage

`hosted_usage_enabled` is an administrator opt-in for exclusive, standard OpenAI
groups with a zero **group default** rate. A zero user-specific override or
`show_account_quota` alone does not enable it. All users granted access to the
enabled group are exempt from platform wallet charges, including users with
an older nonzero rate override. Existing group authorization still applies.

| Persisted amount | Hosted request behavior |
| --- | --- |
| `total_cost` | Existing model-price reference, including the applicable service tier or native Chat fixed-turn price |
| `metered_cost` | Snapshot of that reference, used for Key total and 5h/day/week limits |
| `actual_cost` | Zero platform charge |
| `account_rate_multiplier` | Zero reporting snapshot, excluding the request from configured platform account cost |

HTTP, streaming, WebSocket and native Chat accounting use the same settlement
transaction and request deduplication. Account objects and their global
multipliers are unchanged: paid groups sharing an account retain normal
accounting, and configured upstream account budgets retain their normal basis.
Hosted mode bypasses only wallet eligibility; Key quota, window limits, expiry,
IP, user status and group checks still apply. Existing post-request limit
enforcement remains subject to in-flight request overshoot.

Nullable `metered_cost` preserves historical records. Batch Key analytics adds
`today_usage_cost` and `total_usage_cost` by summing
`COALESCE(metered_cost, actual_cost)`, without changing existing actual-charge
fields or joining current group settings. User Key pages label these amounts
as reference usage. User and administrator usage records and the user CSV export
show reference usage beside the actual charge. Public `/v1/usage` exposes the same per-Key reference through
`usage.range.usage_cost` and `usage.today.usage_cost` for an enabled hosted Key;
`billing.charge_exempt=true` hides unrelated wallet balances while retaining the
legacy billing mode. Upstream account quota percentages remain separate and
may include other groups or off-platform traffic.

Migration 110 defaults every group to disabled and does not backfill usage,
reset windows or alter balances. Enabling/disabling the group preserves prior
counters; earlier zero-charge requests are not retrospectively metered. A Key
with outstanding quota/window usage cannot switch between hosted and billed
groups until the user explicitly resets those counters. This also applies to
administrator rebinding/unbinding. Bulk group replacement across these bases
requires individual Key transitions instead. Missing model pricing
retains the existing unpriced-request policy; reference usage is not a substitute
for an upstream hard quota or concurrency limit.

Before rollout, read-only validation should check historical billing type values
and type/relation inconsistencies. Old rows defaulted to type 0 cannot be reliably
reclassified from a missing subscription ID; do not automatically rewrite them.
