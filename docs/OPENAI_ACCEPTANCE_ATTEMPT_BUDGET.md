# Bounded OpenAI acceptance traffic

`gateway.openai_provider_attempt_budget` is an optional fence for an explicitly
approved acceptance session. It is disabled by default and scopes one fixed
budget to one authenticated Gateway API-key ID, across selected upstream
accounts, connections, transports, retries and Gateway processes.
Only direct OpenAI OAuth and official Platform API-key accounts are accepted
inside the bounded scope. A custom API-key upstream or Gateway relay is
refused, since that next Gateway could multiply one dispatch into hidden
provider retries. Account eligibility is checked again after WS failover.

```yaml
gateway:
  openai_provider_attempt_budget:
    enabled: true
    id: TEST_ONLY-unique-session
    api_key_id: 7
    max_attempts: 5
    expires_at: "2030-01-01T00:00:00Z"
```

The ID is not a credential. The actual session ID, API-key ID, deadline,
authorization and deployment coordinates belong in private operator records.
Configuration supports the equivalent `GATEWAY_OPENAI_PROVIDER_ATTEMPT_BUDGET_*`
environment variables. Invalid enabled configuration fails startup validation.

## Counting boundary

Reservations occur immediately before HTTP model dispatch or an upstream WS
application-frame write. All HTTP Responses attempts (including compact),
final native Chat `/f/conversation` attempts, direct Images model attempts,
pooled WS writes, and dedicated passthrough WS text/binary frames count. Both
client `generate=false` requests and Gateway-generated prewarm writes count.
Unknown WS application frames also count without parsing or rewriting them.

A failed or canceled write still spends its reservation. Reservations are
never refunded after an ambiguous network outcome. An internal retry or
account failover must reserve again. Automatic HTTP redirects and transport
body replay are disabled only for the bounded scope. HTTP body bytes, query,
application headers, WS message type and payload remain unchanged.

Catalogs, account/bootstrap/control requests, file metadata and downloads,
WS handshakes, protocol ping/pong, and token refresh are not model attempts and
do not consume this allowance. The fence does not cap those operations or
provider work initiated independently of this Gateway (for example a hosted
tool server). Acceptance must separately keep such paths inside its approved
scope. One reservation is an application attempt, not a token, image, billed
completion or TCP packet. Count attempts even if output tokens are zero.

## Explicit arming and failure behavior

The selected API key is closed to model traffic until an operator explicitly
arms the Redis record using `ArmOpenAIProviderAttemptBudget`. Request handling
never calls arming. Arming is a separate, approval-bound operator action;
deploying the binary alone neither enables nor arms a budget.

The key is `openai_provider_attempt_budget:` followed by the lowercase SHA-256
hex digest of the session ID. One Lua transaction checks Redis time, the exact
API-key ID, limit and fixed deadline, then increments `used` only below the
limit. Arming uses create-only semantics. The hash fields are `api_key_id`,
`limit`, `expires_ms`, and `used`; the record is retained until one day after
the deadline. Do not edit, delete, renew or reuse an armed session ID. A fresh
session requires a fresh approval and ID.

An exhausted or expired budget returns a local `attempt_budget_*` HTTP 429.
A missing record, unavailable Redis, missing authenticated scope or a policy
mismatch fails closed with HTTP 503. Upgraded WS closes with policy violation;
the Gateway does not retry a budget refusal, alter account schedulability, or
fabricate `response.completed`. Existing handler cleanup releases account
and user reservations. Requested-model metadata remains available for errors.

Restarting the Gateway does not renew the allowance. Loss/eviction of the Redis
record refuses further model traffic rather than silently starting at zero.
There is no in-memory fallback. Normal traffic on other API keys and all
traffic with the feature disabled retain their existing behavior.

## Verification and limits

Local tests cover concurrent reservations, shared HTTP/WS/prewarm consumption,
internal HTTP retry, policy changes, expiry, cache loss, error redaction and
opaque frame preservation. The repository integration suite runs the actual
Lua against isolated Redis; a targeted local run can instead use a task-owned
UNIX socket via `SAIAI_TEST_BUDGET_REDIS_SOCKET`.

This is an acceptance safety mechanism, not evidence that a particular
official Desktop login, model turn, hosted tool or image-generation skill is
compatible. Those still require their own exact-version and surface proof.
