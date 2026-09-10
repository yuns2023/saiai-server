# Native ChatGPT Chat accounting contract

Status: fixed-success-turn accounting is implemented for local/replay
validation. Native ChatGPT Chat remains disabled by default and must not carry
production model traffic until a positive price and the normal release gates
are explicitly approved.

## Evidence boundary

Public OpenAI Responses usage is not the native ChatGPT conversation
protocol. The public Responses stream reports usage in its completed response,
including input, output, cached-input, and total tokens. Those field names and
semantics must not be projected onto `/backend-api/f/conversation`.

For ChatGPT Desktop 26.901.51231, static inspection of the bundled web client
shows:

- the native conversation stream completes with a
  `message_stream_complete` event containing `conversation_id`;
- `[DONE]` is a stream sentinel, not sufficient proof of a successful model
  turn;
- the stream decoder does not define a token-usage object on that terminal
  event; and
- a separate `POST /wham/usage/thread_usage/query` path reads token and
  estimated-credit fields for a set of conversation IDs.

The separate thread-usage path is feature- and plan-gated in that client. Its
result is eventually consistent and cumulative for the whole conversation.
The UI explicitly tolerates an incomplete response and retries it. A single
credentialed staging turn proved that the native conversation completed, but
its raw response was intentionally deleted before token field structure was
captured.

A later non-model probe from the `204.152.198.169` isolated test egress used a
valid Plus OAuth access token and account ID, no Cookie, and a random
conversation ID. The provider returned HTTP 403 with only a top-level `detail`
field. This proves that the thread-usage endpoint is not available under the
Gateway's current minimal OAuth boundary for that account/version/egress. It
does not distinguish plan entitlement from a requirement for additional
Desktop integrity/session state, because the sensitive `detail` value was not
retained. The endpoint is therefore not an approved Plus billing source.

An explicitly capped one-request capture then exercised ChatGPT Desktop
26.901.51231 through the SAIAI local proxy and isolated Gateway to
`chatgpt.com`. The provider returned a complete 200 SSE stream. Schema-only
inspection observed `message_stream_complete` and `[DONE]`, but no input,
output, cached, reasoning, total, credit, or usage counters. The only
usage-like names were a protocol/resume `token` and
`finish_details.stop_tokens`; neither is consumed-token usage. No response
values or message content were retained. Native Chat SSE is therefore also not
an approved token-billing source for this version.

## Current admission rule

`gateway.openai_chat_success_turn_price_usd=0` and
`gateway.openai_chat_unaccounted_allowed=false` are the production-safe
defaults. A final `/f/conversation` request is rejected before account
selection or provider traffic unless the fixed price is positive and finite.
The unaccounted override may bypass that price requirement only with a replay
provider or an explicitly authorized, process-capped credentialed staging
window.

Final model turns detach upstream cancellation from the downstream request but
retain its context values. `gateway.openai_chat_turn_timeout_seconds` bounds
that upstream/drain lifetime and defaults to 600 seconds. A request already
cancelled before forwarding is not sent. This permits a started provider turn
to reach its real terminal event after a client write failure without allowing
an unbounded orphan stream.

`gateway.openai_chat_response_shape_capture=false` is also the default. An
authorized isolated capture window may enable it to log only SSE event types,
top-level field names, immediate `message.metadata` field names, and usage-like
field paths. Values, conversation IDs, message content, and arbitrary message
content keys are not logged. Disable it again when the window closes.

Control-plane requests are not model turns and are never billable.

## Fixed successful-turn contract

One billable unit is one final `/f/conversation` request for which all of the
following are true:

- the upstream HTTP status is 2xx;
- the bounded SSE observer finishes without an error;
- `message_stream_complete` is present with a conversation ID; and
- no provider error event was observed.

`[DONE]` alone is insufficient. Non-2xx responses, rejected requests, control
requests, truncated streams, observer-limit failures, and provider error events
cost zero turns.

The usage row uses model `chatgpt-native-turn`, request type `stream`, zero
input/output/cache tokens, and the configured base price as `total_cost`.
`actual_cost` applies the existing group/user rate multiplier. Subscription
usage, wallet balance, API-key quota/rate limits, and request fingerprinting
use the existing atomic billing transaction and request-ID deduplication path;
usage-log insertion follows the existing idempotent best-effort path. A retry
with the same billing identity cannot charge a second turn. The preferred
billing identity is a SHA-256 digest of the conversation ID and current message
JSON, so Desktop retries remain stable when outer preparation fields change
without storing the conversation ID, message ID, or content. Requests without
a valid message ID fall back to the request-scoped billing ID.

Zero tokens are intentional and must remain visible as zero: request count is
the turn count, while RPM and cost remain meaningful and TPM is not invented.
The incoming `model=auto` value is not used for pricing.

## Usage-source rules

Only provider-reported usage with a versioned, captured schema can become a
token-based billing source. The following are not acceptable substitutes:

- request/response byte length;
- local tokenization of visible text;
- message count or `[DONE]`;
- the requested model value when it is `auto`;
- an undocumented field that merely contains `usage` or `token`; or
- a whole-conversation cumulative snapshot charged as one turn.

The strict thread-usage parser is intentionally separate from `OpenAIUsage`
and `RecordUsage`. It accepts only non-negative integer
`net_new_input_tokens`, `cached_input_tokens`, and `output_tokens` grouped by
an explicit model. Parsing a snapshot does not authorize charging it.

## Required settlement semantics

If `/wham/usage/thread_usage/query` is validated for the selected account type,
per-turn billing still requires all of the following:

1. Capture a baseline snapshot for the selected account and conversation
   before a continuation turn. A conversation without a trustworthy baseline
   is not eligible for delta billing.
2. After a successful `message_stream_complete`, poll the provider usage path
   with bounded retries. Empty/incomplete data remains pending, never zero.
3. Persist snapshots by provider account, conversation, model, reasoning
   effort, and speed. Settle only a non-negative monotonic delta in one atomic
   transaction.
4. Deduplicate the settlement by a stable turn/request identity. A retry must
   not charge twice, and multiple model groups must not collide with the
   existing request-level billing key.
5. Price every delta using the provider-observed model group. Missing model
   mapping or pricing is an accounting failure, not a zero-cost success.
6. Record uncached input, cached input, and output separately. Do not infer
   cache creation usage when the provider did not report it.

First-turn billing needs a provider snapshot that is known to contain only the
new conversation, or an independently captured pre-turn zero baseline. An
existing conversation cannot safely use its first observed cumulative value
as the current turn's usage.

## Completion and failure rules

- A successful terminal event and valid usage settlement are separate facts.
- A non-2xx response, provider error event, truncated stream, or missing
  terminal event cannot confirm conversation affinity or usage.
- Downstream disconnect must not by itself stop upstream draining; the Gateway
  should continue far enough to observe terminal/accounting evidence when the
  upstream connection remains viable.
- Usage parsing, delta persistence, balance deduction, API-key quota updates,
  and the usage log must commit idempotently. Logging success without applied
  billing is not settlement.
- Fixed-turn billing deliberately records zero tokens and the configured
  positive turn price. Any future provider-usage billing mode must treat
  missing usage as an operator-visible accounting failure rather than silently
  recording zero cost.

## Evidence still required before fixed-turn activation

- repeat sanitized native-stream schema capture whenever the supported Desktop
  or private Chat protocol version changes;
- verify disconnect/drain and duplicate-settlement behavior;
- run an isolated end-to-end balance, subscription, API-key quota, and usage-
  log test with exactly one charged turn; and
- explicitly approve the base turn price and production group scope.

## Additional evidence for a future token-based mode

- a successful sanitized response-shape capture of `thread_usage/query` for
  each OAuth plan type intended to use that source; the current Plus/minimal-
  OAuth probe is a 403 and does not qualify;
- before/after snapshots for a two-turn conversation proving cumulative and
  eventual-consistency behavior;
- model-switch, cache, reasoning, tool, and retry cases; and
- an atomic cumulative-snapshot settlement implementation.

The thread-usage parser remains research infrastructure. Production native
Chat billing uses only the explicit successful-turn contract above.
