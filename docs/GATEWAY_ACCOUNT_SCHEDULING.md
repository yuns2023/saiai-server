# Gateway account scheduling and HTTP replay

This document records the public runtime contract for account choice and the
narrow same-account HTTP replay used by the Gateway.

## New-session account choice

The Gateway applies hard eligibility checks first: account status, platform and
model compatibility, account exclusions, concurrency, quota/cost limits, RPM,
and other protocol-specific constraints. It then applies these layers in order:

1. a five-hour admission gate for a new session;
2. the global `accounts.priority` value;
3. for Claude carpool, an existing admission record for the incoming device;
4. current load when a load snapshot is available; and
5. random choice among peers in the same priority/affinity/load layer.

Seven-day usage, remaining quota, reset proximity, `last_used_at`, account type,
and `account_groups.priority` are not soft-ranking inputs. The group-priority
field and its existing list ordering remain available for API compatibility,
but the final runtime choice uses the global account priority.

The five-hour gate rejects a new binding only when the utilization sample and
future reset time are both valid and utilization is strictly greater than 80%.
Exactly 80%, missing or malformed data, and an expired reset time pass the gate.
An existing confirmed or pending sticky session, a previously bound pinned
device, and an OpenAI `previous_response_id` affinity continue on their bound
account.

Codex usage snapshots are serialized by account. Crossing the five-hour
admission boundary is persisted immediately; while a write is active only the
latest pending observation is retained. A failed write causes that latest
observation to be retried, and an older arrival cannot overwrite a newer one.

## Carpool device reuse preference

A valid confirmed or pending session sticky remains first. When choosing a new
account, within the same global account-priority tier, prefer bounded carpool
accounts whose admitted-device registry already contains the incoming
`metadata.user_id.device_id`. Among those accounts choose the lowest load and
break ties randomly. If they cannot accept the request, try other eligible
accounts using the same priority/load/random rules. Model-routing boundaries
and their existing fallback policy still apply.

The preference applies to normal, model-routed, and legacy selection, including
load-cache failure and fallback wait ordering. It does not bypass account,
model, concurrency, session, RPM, cost, or quota admission checks. In particular,
a device record alone does not exempt a new session from the five-hour gate.
A busy preferred account does not force a new session to wait when another
candidate can acquire a slot; existing sticky wait behavior is preserved.

Membership reads use a Redis pipeline against the admitted registry only.
Overflow entries, another device's records, and retained records on unlimited
accounts confer no preference. Other traffic modes get no preference. Missing
or invalid device metadata and lookup errors retain ordinary scheduling.
Membership is memoized only for one selection call, so later requests observe
device removal or daily rotation. The lookup never creates records or refreshes
`last_seen_at`; the existing atomic forwarding-time admission check still owns
registration, full-registry rejection and failover. Existing devices may reuse
a full registry. Concurrent admission or eviction can change a lookup result
before that final check; the hint never reserves a device slot.

## Carpool device admission

Anthropic OAuth and setup-token accounts in `carpool` mode normally admit a
bounded number of distinct devices. The account-extra field
`claude_oauth_carpool_device_limit` defaults to 5 and is constrained to 1..32.

Setting the explicit account-extra boolean
`claude_oauth_carpool_unlimited_devices` to `true` disables only this local
device-count admission gate. Official-client request-shape checks, billing
integrity validation, deterministic per-account device identity rewriting,
sticky routing, concurrency, quota, and upstream rate limits remain active.
The switch is ignored by `shared`, `pinned`, and `single_device` modes.

Unlimited mode does not add devices to the non-expiring bounded-mode registry.
Existing recorded and overflow entries are preserved so bounded mode can be
enabled again without silently discarding operator state.

Setting `claude_oauth_carpool_auto_expand_enabled` to `true` enables daily
bounded-registry maintenance at 00:00 in the configured server timezone. While
the configured device limit is below 16, the worker increases it by one per
calendar day, up to 16. At a configured limit of exactly 16, the worker leaves
the administrator's limit unchanged and, only when the recorded-device count
has reached that limit, removes one device with the oldest `last_seen_at`.
When the configured limit is above 16, the administrator owns the limit and
device lifecycle; the daily worker does not change or evict anything.
Ties are resolved by `created_at` and then the hashed device key.

The daily operation is idempotent per account and calendar day. A removed
device has no cooldown or deny-list entry and may immediately register again
through normal admission if a slot remains free. Administrators may delete
recorded devices at any time. The switch is ignored in unlimited mode and does
not automatically increase an administrator-configured limit beyond 16.

## User-scoped Claude Code device limits

Claude Code device capacity belongs to the SAIAI user. `users.claude_device_base_limit`
defaults to 3 and administrators may set it to 1..100 in user management.
This user capacity is separate from `claude_oauth_carpool_device_limit`, which
limits the devices admitted to one upstream carpool account and remains an
account-level setting. Changing user capacity does not enlarge an account pool.
Every API key and group owned by the same user shares this capacity. An active
device hash is counted once across groups; revoking it revokes all historical
group registrations. Existing active devices remain usable after lowering capacity,
but new devices are rejected until capacity is available. Generic API traffic is
not subject to Claude Code device admission. The official token-counting metadata
exception is preserved.

Migration 106 retains historical registrations and sums redeemed group bonuses
into `user_claude_device_quotas`. New device redemption codes need no group and add
to user-wide bonus capacity. Legacy group device fields are retained for decoding
but no longer determine admission. Old and new Gateway versions must not run
concurrently while changing this admission contract; rollback needs an explicit
plan for bonuses redeemed after migration rather than a database restore.

The user list displays distinct active devices / base plus bonus capacity,
including 0 / 3 for a new user. Encrypted device IDs remain administrator-only;
normal logs contain only a one-way reference and the last four characters.

Device-related structured logs use `user_id`, an optional username snapshot,
`device_ref`, and the last four characters of the device ID. `device_ref` is a
stable one-way correlation value; the raw device ID is never written to normal
application or request logs. Registration, reconnect, limit rejection, audit
overflow, revoke, bonus-quota, and input-moderation decision events may carry
these fields.

## Single-device setup-token identity

Anthropic OAuth accounts in `single_device` mode require both a fixed account
UUID and a fixed device ID. Setup-token accounts require the fixed device ID
but do not accept a fixed account UUID: their outbound `metadata.user_id`
always carries an empty `account_uuid`, even when the inbound request or stale
account-extra data contains a non-empty value.

The admin create/edit forms therefore ignore and remove `account_uuid` for a
setup-token account in `single_device` mode. This section does not redefine the
identity rules of the other OAuth modes.

Administrator-supplied fixed headers have a separate enable switch, disabled
when absent. Migration 097 explicitly enables the switch for existing
`single_device` accounts with non-empty fixed header text and no prior flag.
New accounts therefore default to disabled even when text is supplied through
the API. Turning the switch off moves text to
`claude_oauth_fixed_headers_saved_text` and removes the old active-text key.
The editor retains the text for later use, while a rollback to an older Server
cannot reactivate it. Without a fixed `User-Agent`, incoming UA variants
continue updating their slots.
When an enabled fixed header supplies `User-Agent`, its value selects the UA
slot and incoming UA versions do not update that slot's fingerprint.

An optional incoming-device admission registry counts the original client
`metadata.user_id.device_id`, independently of UA slots and the single fixed
upstream device ID. It is disabled by default for existing accounts. When
enabled, its limit defaults to 5 and is constrained to 1..32. A separate
daily-maintenance switch raises the limit by one each calendar day in the
configured server timezone until the account target (default 16). Once the
limit equals the target, a full registry loses its oldest `last_seen_at`
device at 00:00. A limit above the target is administrator-controlled. The
evicted client can immediately register again if capacity remains; no
cooldown or deny-list is created. This maintenance never rotates the fixed
upstream device ID or the UA slots. Administrators can inspect the separate
incoming-device registry and remove an entry in the account editor; the
dedicated admin API uses `/claude-single-device-admissions`.

## Claude OAuth request attribution

Every Anthropic OAuth/setup-token forwarding attempt emits the structured
`claude_oauth_request_attribution` event after request identity preparation.
The event is indexed under the `audit.claude_oauth_attribution` component, so
administrators can query it through the existing Ops system-log API and UI.
It is written directly to the bounded Ops log sink and is therefore not
suppressed by the normal runtime log level or sampling configuration. Sink
queue drops and database write failures remain visible through the existing
system-log ingestion health counters.
The event contains only the internal request ID, selected account ID, account
type, traffic mode, request kind, preparation stage, and these routing facts:

- `selection_source` is `scheduler`, `sticky_confirmed`, `sticky_pending`,
  `failover`, or `unknown`; the legacy `sticky` value remains queryable for
  events written before this distinction was introduced;
- `identity_prepared` and `identity_rewritten` report whether the OAuth
  identity was prepared and whether the request bytes changed;
- `native_billing` identifies the native billing-style path; and
- `transport_isolated` reports whether a derived transport isolation ID was
  used instead of the selected account ID.

Upstream error-attempt JSON carries the same values under `oauth`, so a
recovered retry or failover remains attributable to the account attempt that
experienced it. The attribution schema accepts only fixed account-type,
traffic-mode, selection-source, and request-kind enums. It never stores token
values, metadata device/session/account UUIDs, email context, fingerprints,
rewritten headers, request bodies, or transport isolation IDs.

`GET /api/v1/admin/ops/system-logs` accepts the bounded filters
`oauth_account_type`, `oauth_traffic_mode`, `oauth_selection_source`,
`oauth_request_kind`, and `oauth_stage`. Supplying any of them automatically
limits the query to the OAuth attribution audit component. The paginated
response `total` is the matching count for the selected time window. The same
filters are supported by the filtered cleanup endpoint so an OAuth-filtered UI
cleanup cannot accidentally delete a broader set of logs.

## Same-account HTTP replay

Before any response bytes have been sent, the initially selected account gets
at most one same-account replay for HTTP `500`, `502`, `503`, or `504`. This
applies to standard Claude forwarding, Anthropic API-key passthrough, Bedrock,
OpenAI Responses, and OpenAI Messages compatibility forwarding.

The replay does not apply to HTTP `501`, `505`, or `529`, transport errors, a
response that has already started, or any request after an account switch. A
normal HTTP `429` also goes directly through the account-failover policy; the
only exception is the reset-less, transient OpenAI `429` retry described below.
After the one replay is consumed, the existing account-failover policy may
still choose a different account; the dedicated replay budget is never
restored by that switch.

All tests for this behavior use local mock upstreams and do not issue provider
model requests.

### Reset-less OpenAI 429 retry

OpenAI HTTP `429` responses with no parseable reset metadata may represent a
short-lived burst. When the response body does not identify an explicit quota
or usage-limit failure, the Gateway retries the same request at most five total
attempts (the initial request plus four retries), waiting three seconds between
attempts. This retry is bounded by the request context and only runs before
any client response has started.

After the fifth failed attempt, the Gateway returns HTTP `429` to the client
and applies the normal short account cooldown. A later series of independent
failures can still reach the existing account-level escalation policy. Parsed
reset metadata, `insufficient_quota`, `usage_limit_reached`, and equivalent
quota messages keep their normal failover/rate-limit handling. OpenAI WebSocket
handshake/reconnect behavior is governed by its separate WS retry policy.

### OpenAI Responses WebSocket quota failover

Official Codex OAuth WebSockets suppress an explicit upstream quota error only
when the current turn has emitted no downstream frame and a complete local
replay input is available. The exhausted account is persisted/excluded, a
transport-compatible replacement is selected, account-bound continuation state
is removed, and the reconstructed turn is sent on a new upstream WebSocket
without closing the downstream client connection.

The retry is bounded to three account switches. It does not apply to native
relay accounts, unresolved `item_reference` inputs, missing or oversized replay
state, ambiguous pipelined turns, or a turn that already emitted any frame.
Those cases preserve the normal client-visible failure rather than replaying a
possibly billable or side-effecting turn.

## Account-scoped device authorization failures

An Anthropic-compatible HTTP `400` that says the upstream device authorization
has been unbound or revoked, or reports the equivalent branded client-state
restart failure, is classified as account state, not as a malformed customer
request. The Gateway marks that account unavailable and enters normal account
failover without replaying the request on the same broken account.

The raw upstream recovery instruction is retained only in restricted operator
diagnostics. It is never returned through a client error-passthrough rule. If
all eligible accounts fail, the client receives HTTP `502` with a neutral SAIAI
service-channel message.

Restricted upstream provider identities are protected by a non-configurable
final response boundary across JSON, SSE, raw `400`, and configurable
error-passthrough paths. An identity match alone redacts the client response;
account isolation still requires the narrower account-state classification so
an unrelated error cannot disable an otherwise healthy account.

## Carpool user access

`claude_oauth_carpool_trusted_only` and `claude_oauth_carpool_users_only` are
independent account switches for Anthropic OAuth/setup-token carpool. Enabled
conditions are combined with AND. An enabled empty user list denies everyone.
Membership is stored in `claude_oauth_account_users` with user/account foreign keys,
and exposed to administrators as `carpool_user_ids`. The authenticated SAIAI Key
owner supplies the user ID, never client metadata. Selection, sticky reuse and
forwarding apply the same predicate. Existing group permissions, request-shape
validation, device capacity and upstream limits continue to apply.
