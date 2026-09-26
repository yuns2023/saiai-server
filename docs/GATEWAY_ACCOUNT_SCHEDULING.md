# Gateway account scheduling and HTTP replay

This document records the public runtime contract for account choice and the
narrow same-account HTTP replay used by the Gateway.

## New-session account choice

The Gateway applies hard eligibility checks first: account status, platform and
model compatibility, account exclusions, concurrency, quota/cost limits, RPM,
and other protocol-specific constraints. It then applies these layers in order:

1. a five-hour admission gate for a new session;
2. the global `accounts.priority` value;
3. current load when a load snapshot is available; and
4. random choice among peers in the same priority/load layer.

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

The group fields `claude_device_limit_mode` and `claude_device_base_limit`
limit a SAIAI user within that group. They are independent of the
account-scoped carpool registry above. Admission is keyed by `(user_id,
group_id, device_hash)` and redeem codes add bonus capacity in the
`user_group_claude_device_quotas` table.

The registry keeps the hash for admission and stores the original
`metadata.user_id.device_id` encrypted with the configured TOTP AES-256-GCM
key for administrator-only device management. Existing rows created before
the encryption column was added have no recoverable original ID until the
device connects again. The production key must be stable across restarts;
an auto-generated development key cannot decrypt previously stored IDs.

The admin user list exposes `claude_device_count` and `claude_device_limit` as
an aggregate for the current page. The count includes only active registrations
and the limit includes base plus redeemed bonus capacity across groups with
device limiting enabled; revoked registrations are excluded. A missing limit
means that no device-limited group is represented for that user. Device IDs
remain available only in the administrator device-detail view.

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
cannot reactivate it. Incoming UA variants continue updating their slots.

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
