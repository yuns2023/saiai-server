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

## Same-account HTTP replay

Before any response bytes have been sent, the initially selected account gets
at most one same-account replay for HTTP `500`, `502`, `503`, or `504`. This
applies to standard Claude forwarding, Anthropic API-key passthrough, Bedrock,
OpenAI Responses, and OpenAI Messages compatibility forwarding.

The replay does not apply to HTTP `501`, `505`, `429`, or `529`, transport
errors, a response that has already started, or any request after an account
switch. After the one replay is consumed, the existing account-failover policy
may still choose a different account; the dedicated replay budget is never
restored by that switch.

All tests for this behavior use local mock upstreams and do not issue provider
model requests.

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
