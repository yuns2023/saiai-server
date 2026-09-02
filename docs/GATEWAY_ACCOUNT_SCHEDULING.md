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
