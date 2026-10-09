# Native ChatGPT Chat accounting contract

Status: fixed-success-turn accounting is implemented and its balance/dedup
contract is validated against the isolated replay database. Native ChatGPT Chat
remains disabled by default and must not carry production model traffic until a
positive price and the normal release gates are explicitly approved.

## Evidence boundary

macOS Desktop 26.930.51102 requests `GET /ios/attestation_challenge` through
its native backend client before generation; Windows/web preparation uses the
separate Sentinel POST. The managed route is
`GET /chatgpt/backend-api/ios/attestation_challenge`. It forwards the real
provider challenge and integrity headers rather than returning an empty local
success. It is a control request with no model-turn charge or retained model
slot. The unchanged challenge response (including its encoding) is inspected
only to bind a scoped digest to its OAuth owner for five minutes. A generation
carrying `app_attest_challenge` must use that owner and agree with any existing
conversation or attachment ownership. Unknown, expired or foreign challenges
fail before generation; the Gateway never fabricates integrity tokens or
silently switches accounts. Multi-account continuation with an incompatible
challenge is rejected, not repaired by altering the client's integrity data.
These local replay checks do not establish actual Apple DeviceCheck/provider
acceptance on an installed Desktop.

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

Administrators can adjust five model-independent successful-turn prices at
**Settings → Gateway → ChatGPT Chat and image billing**. The admin-only
`GET`/`PUT /api/v1/admin/settings/chatgpt-billing` resource contains
`success_turn_price_usd` (automatic/unrecognized fallback) and an optional
complete `tier_prices_usd` table with `instant`, `medium`, `high`, `extreme`,
and `pro`. An optional complete `image_prices_usd` table contains `1K`, `2K`,
`4K`, and `unknown` surcharges per recognized completed image. Omission on PUT
preserves the existing image table, so an older UI cannot silently disable it;
explicit `null` disables image surcharges. On an installation that never
enabled them, omission keeps the legacy image-inclusive turn price. A legacy
PUT with only the fallback field still selects uniform Chat turn pricing.
Prices must be numeric, finite and non-negative; an explicit zero Chat price disables
requests in that tier, while a zero image price waives only that image surcharge.
Neither opts into free/unaccounted model turns. Missing,
null or unknown entries in a supplied tier table are rejected. Group/user
multipliers still apply. These prices are retail turn prices, not measured
provider consumption. Responses/Codex token pricing is independent.

The native Chat request is inspected without rewriting its bytes. Desktop
26.1002.6548.0 was observed with five presets: an instantaneous model with no
thinking effort, `standard`, `extended`, `max`, and a Pro model with no effort.
The first maps to `instant`; the three explicit native efforts map to
`medium`, `high`, and `extreme`; a Pro model maps to `pro`. Unknown presets and automatic choices without an explicit recognized effort
use the fallback. An explicit standard/extended/max effort keeps its selected
price even when the model choice is automatic. Do not insert synthetic effort fields.

The persisted `openai_chat_success_turn_price_usd` key atomically stores either
the legacy numeric price or the whole JSON price table. It overrides startup
configuration, including zero; only an absent setting uses startup pricing.
Database failures or malformed settings fail closed before provider traffic.
Every admitted turn snapshots its initial model, effort, Chat and image prices, billing identity
and selected account in shared Redis. Retries/resumes preserve that snapshot;
a later admin save never reprices it. Metadata changes using the same message
identity are rejected. Records expire after one hour and contain hashes and
accounting metadata, not content, conversation IDs, credentials or resume tokens.
An older Gateway cannot read tier JSON: rollback requires restoring the scoped
legacy price through this admin resource before rolling back the application.
No schema migration or whole-database restore is needed.

`gateway.openai_chat_success_turn_price_usd=0` and
`gateway.openai_chat_unaccounted_allowed=false` are the production-safe
defaults. A final `/f/conversation` request is rejected before account
selection or provider traffic unless the fixed price is positive and finite.
The unaccounted override may bypass that price requirement only with a replay
provider or an explicitly authorized, process-capped credentialed staging
window.

Direct OpenAI staging keeps the upstream override empty. It requires all three
of `openai_chat_unaccounted_allowed=true`, an enabled shared provider-attempt
budget, and a positive `openai_chat_model_request_cap`. The process cap does
not arm, reset, or replace the Redis fence: its API-key scope, allowance, fixed
deadline and armed state are enforced again immediately before provider send.

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

Catalog, initialization, preparation and assets are not model turns. Resume
requests deliver a previous turn; a successful resume settles that original
turn once, rather than creating another billable unit. When native
Chat is explicitly enabled, catalog, initialization, preparation, and asset
requests do not require a model-turn price or a replay upstream. They still
require an authenticated OpenAI group and a schedulable OAuth account. The
final `/f/conversation` request retains its separate billing/staging gate.

## Native image generation within a Chat turn

ChatGPT can generate images inside its native conversation stream. This does
not imply a separate public Images API request. A successful turn containing a
recognized image tool retains the same billing identity. With image surcharges
disabled, it retains the original Chat-only price. With them enabled, one
atomic debit includes the initial Chat fee plus the sum of the snapshotted
per-image prices. `media_type=image` and `image_count` describe recognized
completed assets. Hashes deduplicate repeated assets across handoff/resume.
The first successful terminal delivery atomically seals the count, observed
dimensions, and image fee before queueing settlement; later metadata cannot
change that debit or create a fingerprint conflict. Downloads and repeated
asset opens remain control requests and create no additional usage record.

Resolution brackets use both observed output dimensions and the longer edge:
`1K` is at most 1024 pixels, `2K` is at most 2048, and `4K` is at most 4096.
For example, 1536×1024 falls in the 2K price bracket; its displayed dimensions
remain 1536×1024. Missing, contradictory, invalid or larger dimensions use the
explicit `unknown` price, never an inferred 2K price. Generation and editing
share this table. Format, quality, thinking effort and requested size are not
used to guess output dimensions or image prices. Configuring a 4K price does
not advertise upstream 4K support or alter any provider request.

The existing usage-log size column holds at most ten characters. Observed
dimensions too long for that field remain in the sealed turn snapshot and use
the normal unknown-resolution tariff, while the usage row displays unknown
dimensions. They must not cause the entire usage insert to fail after a debit.

The bounded observer accepts known image tool messages, generated multimodal
asset parts, explicit image-part patches for an identified tool message, and
completed structured `image_generation_call` items. User uploads, unrelated
tools, failed images and incomplete previews are excluded. Only SHA-256 asset
digests and bounded observed dimensions are retained for deduplication across
handoff/resume, with a maximum of 64 observed assets, an 8 KiB snapshot bound,
and the original one-hour turn expiry. Raw pointers, image
bytes, prompts and tool content are not persisted.

Image counts describe the recognized response schemas. Opaque deltas, implicit
patch paths and future schemas are forwarded unchanged and may leave the
completed count unknown or incomplete. A known image tool with no recognized
completed asset is displayed as image generation with an unconfirmed count,
not as a proven zero. The UI labels positive counts as observed completed
images and keeps the Chat/image charge components and unknown token usage visible.
Mock fixtures and the installed Desktop renderer establish this schema
boundary; they are not a new credentialed image-generation acceptance. Existing
historical rows are not retroactively inferred from download activity.

## Fixed successful-turn contract

One billable unit is one native user-message generation whose initial
`/f/conversation` stream or associated `/f/conversation/resume` stream has all
of these signals:

- the upstream HTTP status is 2xx;
- the bounded SSE observer finishes without an error;
- `message_stream_complete` is present with the expected conversation ID; and
- no provider error event was observed; and
- no unresolved native async work remains. A stream terminal with active,
  malformed or unknown async state is not a completed user turn.

The experimental background-delivery candidate preserves pending state across
delivery legs and requires an owned current conversation snapshot before
settlement. An HTTP terminal or resume token alone cannot establish completion
in this mode. Download ownership for observed previews remains separate from
completed-image billing. See [the native updates contract](CHATGPT_NATIVE_UPDATES_CONTRACT.md)
for the feature gate, isolation, intentional delivery transformations and
remaining official-client acceptance boundary.

An initial `resume_conversation_token` or `stream_handoff` with a conversation
ID establishes delivery ownership before its chunk is flushed to Desktop.
A resume envelope may contain native delivery extensions and an offset, but
must not contain new generation input. The same authenticated user, Key and
group must own the pending record, and its OAuth account must still be eligible
in that group. Missing/expired contexts and unavailable owners fail closed.
Overlapping unsettled handoffs cannot overwrite one another. Completion
promotes ordinary conversation affinity. Resumes acquire bounded user/account
stream slots and release them on termination; background provider work between
HTTP delivery legs is not an active Gateway stream slot.

`[DONE]` alone is insufficient. Non-2xx responses, rejected requests, control
requests, truncated streams, observer-limit failures, and provider error events
cost zero turns.

The usage row retains the actual requested model, the observed assistant model
when different, and the original native thinking effort. The atomic billing
command keeps its stable `chatgpt-native-turn` namespace and original message
fingerprint. Rows use request type `stream`, placeholder zero tokens, and the
admitted Chat fee plus the sealed image surcharge as `total_cost`. For native
rows only, existing `input_cost`/`output_cost` persist the Chat/image base cost
components without adding database columns or inventing tokens. APIs expose
their effective charges after multipliers as `native_chat_turn_cost_usd` and
`native_chat_image_cost_usd`; user/admin/public-Key views label these explicitly.
Legacy rows with no output cost retain a zero image surcharge and are never
repriced from current settings. APIs identify native rows with
`billing_unit=turn`, `token_usage_source=unknown`, and their `chat_tier`; the
usage tables display one successful turn and unavailable token consumption,
rather than interpreting placeholder zeros as measured usage.
`actual_cost` applies the existing group/user rate multiplier. Subscription
usage, wallet balance, API-key quota/rate limits, and request fingerprinting
use the existing atomic billing transaction and request-ID deduplication path;
usage-log insertion follows the existing idempotent best-effort path. A retry
with the same billing identity cannot charge a second turn. A full or stopped
usage queue falls back to synchronous native-Chat settlement; a terminal marker
permits the next turn while an accepted billing job waits in the queue. The preferred
billing identity is a SHA-256 digest of the conversation ID and current message
JSON, so Desktop retries remain stable when outer preparation fields change
without storing the conversation ID, message ID, or content. Requests without
a valid message ID fall back to the request-scoped billing ID.

Zero tokens are intentional and must remain visible as zero: request count is
the turn count, while RPM and cost remain meaningful and TPM is not invented.
The incoming `model=auto` value is not used for pricing.

An isolated PostgreSQL/replay validation at a base price of USD 0.02 observed:

- first message: balance 100.00 to 99.98, one usage row, one dedup row;
- exact retry of the same message in a new HTTP request: no additional balance,
  usage, or dedup change; and
- a different message: balance 99.98 to 99.96, two total usage and dedup rows.

Both usage rows had 64-character hashed request IDs, zero input/output tokens,
request type `stream`, and total/actual cost 0.02 at multiplier 1. A preliminary
77-character prefixed identity applied billing but exceeded the legacy
`usage_logs.request_id` width; the test state was reset and the final contract
uses the full 64-character SHA-256 hex digest without a prefix.

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
- run isolated database settlement coverage for subscription and API-key quota
  modes (balance, usage-log, disconnect/drain, and duplicate settlement are
  already covered); and
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
