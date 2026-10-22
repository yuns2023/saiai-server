# Codex usage limit resets

OpenAI OAuth accounts have a **Reset Codex upstream limits** action in the
administrator account menu. It lists available earned resets, their expiration,
and redeemed/redeeming records. **Use reset** consumes the selected credit and
can restore the account's 5-hour and weekly upstream limits. Merely opening or
refreshing the dialog never consumes a reset.

This is separate from **Clear Local Quota Snapshot** (local observations only)
and **Reset Quota** (the Gateway's API-key billing quota). It does not purchase
credits, create reset entitlements, or make model requests. Accounts without a
valid OpenAI OAuth identity cannot use it.

## Control API

Both routes require the existing administrator authentication middleware:

- `GET /api/v1/admin/accounts/:id/codex-reset-credits`
- `POST /api/v1/admin/accounts/:id/codex-reset-credits/consume`, with
  `credit_id` and `idempotency_key`.

The Server follows the official Codex `rust-v0.162.1` backend-client contract:

- `GET https://chatgpt.com/backend-api/wham/rate-limit-reset-credits`
- `POST .../rate-limit-reset-credits/consume`, with `credit_id` and
  `redeem_request_id` (the caller's idempotency key).
- `GET .../usage` after a definitive `reset`, `already_redeemed`, or
  `nothing_to_reset` outcome, to reconcile usage without a model probe.

Requests use the account's OAuth token provider (including its refresh flow),
ChatGPT account ID, and configured proxy. Redirects are rejected. Provider error
bodies and unused response fields are not exposed to the administrator.

The dialog stores the selected credit and request ID in tab session storage
before sending. An uncertain response or incomplete local synchronization keeps
that pair for retry, including after closing/reopening or reloading the page.
Other credits are disabled while an operation remains unresolved. Upstream
idempotency, rather than a newly generated ID on retry, prevents spending again.

The response preserves all four official outcomes: `reset`, `nothing_to_reset`,
`no_credit`, and `already_redeemed`. A consumed reset with a failed usage fetch or
database update still returns its definitive outcome and `quota_synced: false`.
It must not be presented as a failed redemption or trigger a new redemption.

Only complete, valid 5-hour/weekly usage windows update the local Codex snapshot.
The local account rate-limit flag is cleared only when the authoritative usage
response explicitly says `allowed: true` and `limit_reached: false`. An exhausted
window retains a valid upstream reset time. Missing/malformed usage preserves
local scheduling state. Error, overload, manual scheduling, temporary exclusions,
and unrelated quota fields are not cleared by this operation.

Within one Gateway process, reset reconciliation waits for in-flight snapshot
writes and rejects queued snapshot/rate-limit writes observed before its usage
fetch. The same boundary invalidates old snapshot throttle decisions. This
covers response-header snapshots, 429 handling, usage-page probes, account
tests and rate limits derived from a stored snapshot; a stored snapshot older
than the reset no longer marks the account limited. Request paths never wait
for a reconciliation: when the account is busy, their write is queued with the
observation time at which it was received. This is process-local
coordination; it does not serialize independent Gateway replicas.
Subsequent model traffic can report newer usage; the reset does not reserve
capacity.

## Evidence

See [pricing and quota review notes](PRICING_AND_QUOTA_REVIEW.md) for controlled
write-order tests, coordination limits and resource-safe validation commands.

- [Official backend-client reset methods and paths](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/backend-client/src/client/rate_limit_resets.rs)
- [Official payload and outcome regression fixtures](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/backend-client/src/client/rate_limit_resets_tests.rs)
- [Official app-server consumption and idempotency handling](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/app-server/src/request_processors/account_processor/rate_limit_resets.rs)

Local Go HTTP mocks cover authentication headers, paths, selected-credit payload,
idempotent retries, outcomes, authoritative snapshot updates, unsupported account
types, response redaction, and failed refresh/persistence. Vue tests cover action
scope, credit eligibility, duplicate clicks, safe retries across dialog reopening,
and partial synchronization. No production reset or live model request is used
for development validation; actual provider availability remains unverified.
