# Native ChatGPT Chat accounting contract

Status: research/staging only. Native ChatGPT Chat must remain disabled for
production model traffic until this contract has a verified provider usage
source and an atomic settlement implementation.

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

## Current admission rule

`gateway.openai_chat_unaccounted_allowed=false` is the production-safe
default. It rejects final `/f/conversation` requests before account selection
or provider traffic. The override may be used only with a replay provider or
an explicitly authorized, process-capped credentialed staging window.

`gateway.openai_chat_response_shape_capture=false` is also the default. An
authorized isolated capture window may enable it to log only SSE event types,
top-level field names, immediate `message.metadata` field names, and usage-like
field paths. Values, conversation IDs, message content, and arbitrary message
content keys are not logged. Disable it again when the window closes.

Control-plane requests are not model turns and are never billable.

## Usage-source rules

Only provider-reported usage with a versioned, captured schema can become a
billing source. The following are not acceptable substitutes:

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
- A completed model turn whose usage remains unavailable must raise an
  operator-visible accounting failure. Production must not silently record
  zero tokens or zero cost.

## Evidence still required before activation

- a sanitized, key-path-only capture of the native stream terminal events;
- a successful sanitized response-shape capture of `thread_usage/query` for
  each OAuth plan type intended to use that source; the current Plus/minimal-
  OAuth probe is a 403 and does not qualify;
- before/after snapshots for a two-turn conversation proving cumulative and
  eventual-consistency behavior;
- model-switch, cache, reasoning, tool, and retry cases;
- disconnect/drain and duplicate-settlement tests; and
- an isolated end-to-end balance/quota/usage-log test with exactly one charged
  turn.

Until these gates pass, the implemented parser is research infrastructure, not
a production billing path.
