# Input moderation results and Jev settings

Group moderation remains opt-in. The newest real user text and transmitted project instructions are classified.
See the coverage and content-cache contract below.

Usage history exposes `moderation_result` independently of risk events:

- `pending`: submitted for asynchronous processing.
- `passed`: completed below the group's risk threshold; all returned category
  scores and the evaluated threshold are retained.
- `flagged`: at least one category meets the threshold; existing risk-event and
  enforcement rules still apply.
- `skipped`: no new user text, a disabled group, or an administrator-marked
  trusted user (`trusted_user`).
- `failed`: processing failed, the queue was full, or the job expired. Durable
  processing failures remain retryable and a later success replaces failure.

Results contain no prompts, provider responses, credentials, or raw errors.
Correlation includes request ID, source, turn, user, API key, and group. Terminal
passed/flagged results cannot be overwritten by late pending/failure writes.
Successful results never create risk events or strikes. Historical usage without
result metadata remains explicitly unknown, not retroactively marked passed.

Migration 103 adds the result metadata table; apply it before this application
version. User, API key, or group deletion cascades to the associated results.
Rollback does not require dropping the additive table or restoring the database.

## Admin configuration

Settings → Gateway → Jev input moderation uses admin-only `GET/PUT
/api/v1/admin/settings/jev`. The endpoint accepts a complete HTTPS URL without
credentials, query, or fragment. Saving performs no provider request.

PUT fields are `endpoint`, `api_key`, and `clear_key`. An empty API key preserves
the effective key; explicit `clear_key` removes it without falling back to the
environment. Changing the URL requires a replacement key or explicit removal,
so an existing credential is not silently forwarded to a new address. Saved
configuration selects Jev; the model remains pinned to `jev-1.13.0`.

Before the first admin save, existing environment configuration remains effective.
After saving, the database configuration takes precedence for subsequent jobs
across replicas without restarting. Jobs already in flight finish with their
resolved configuration. Database/decryption errors fail moderation instead of
silently using an old key. The response exposes only `key_configured`, never the
key or ciphertext. The database stores the key encrypted with AES-GCM using the
existing stable server encryption key; saving is disabled without that key.

Clearing the key causes subsequent text moderation jobs to fail until a key is
configured again. Group switches and enforcement settings are not changed by
saving provider configuration. This feature does not make moderation synchronous
or block the original model request while classification runs.

## Trusted users and usage presentation

Administrator-managed `is_trusted` exempts a user from input classification.
The worker reads the current user record before calling the detector, including
for previously queued jobs; it does not rely on an old authentication snapshot.
It checks trust again after inference, so promotion while a request is in flight
prevents a new risk event or penalty. Revocation resumes normal processing for
subsequent jobs. An already-started detector call cannot be undone. This rule
does not erase old events or automatically lift existing cooldowns/disables.
User lookup failures produce a failed job, never an assumed trusted exemption.

The usage list shows status with the highest recorded risk score for passed
results, and only threshold-matching category labels for flagged results. Missing
scores are not presented as zero. Category labels follow the UI language; unknown
historical category IDs remain visible. Click the status for all recorded scores,
thresholds and actions.
Trusted exemptions remain skipped rather than passed. The list does not poll;
use the refresh button to load newly completed asynchronous results. Filters
and pagination continue to load their requested data.

## Administrator risk counts and filtering

User management shows **Input risk alerts (30 days)**. This is the number of
persisted `flagged` detection results in the rolling 30-day window, measured by
completion time (`updated_at`, inclusive start/exclusive end). Each unique
request/source/turn/user/key/group identity counts once, regardless of the number
of matching categories or enforcement strikes. WebSocket turns are separate
inputs. Retried writes cannot multiply terminal results. Historical results use
their saved decisions; changing today's policy does not reclassify them.
Unknown/failed/pending/skipped detections are not alerts. An unavailable counter
is shown as unknown, not zero. Deleting a user/key/group cascades its existing
moderation metadata; these counts are not an immutable compliance archive.

Clicking the count opens usage history with the same user and exact window and
**Only input risk alerts** enabled. The usage list, pagination, CSV export and
usage summary use the full moderation identity to filter matching billed rows.
All existing list filters remain active. Charts without moderation filtering
and the broad usage-cleanup action are hidden while the alert filter is active.

An additional paginated input-alert history includes detections without a billed
usage row (for example a failed upstream request), with saved scores, thresholds
and any recorded enforcement action. It filters by user and detection completion
time only, as stated in the UI; usage rows below still filter by request time and
all selected usage criteria. Thus the two lists may have different totals.
Detection is asynchronous, so refresh to see newly completed results. A flagged
result alone does not mean the original request was blocked.

Admin-only API additions:

- User list `input_risk_alerts`: `{count,start_time,end_time}` or null if unavailable.
- Usage list/stats `moderation_flagged=true` (strict boolean query).
- `GET /api/v1/admin/usage/input-risk-alerts`: optional positive `user_id`, required
  RFC3339 `start_time`/`end_time`, and standard `page`/`page_size` pagination.
  Returns only correlation and detection metadata, never input text.

Migration 105 adds partial indexes for flagged-result counts/history. It changes
no moderation, penalty, billing or provider-request behavior. This feature does
not automatically suspend users based on the displayed count.

## Project instructions, long inputs and content cache

Anthropic extraction includes system text, system/developer messages, historical
user messages identifying CLAUDE.md, AGENTS.md or user_instructions, and structured
Read/read_file results correlated to those filenames. Recognized project
instruction envelopes in the latest user message are separated for stable reuse;
malformed or unrecognized envelopes remain in the assessed user text. Responses HTTP and WebSocket
extraction includes top-level instructions, system/developer input messages and
historical user instruction messages with those markers. Ordinary assistant
history, unrelated tool output and images remain excluded. The Gateway cannot
inspect local files or recover instructions absent from the current request
(including server-held previous_response_id context). Arbitrary shell file reads
are not reliably identified. A filename is never a trust or exemption signal.

Stable instruction text is classified separately and also joined with the new
user text for context assessment. A safe cached document cannot exempt a new
question. Content changes invalidate reuse. Small combined inputs use one joint
assessment; long inputs use overlapping Unicode-character chunks (default
32,768 characters, up to 512 characters overlap). Each category retains the maximum
score across all required checks, then applies the current group threshold once.
Cross-chunk semantics are limited to the overlap, not full-document equivalence.
Passed chunked results therefore display **Chunk checks passed**, with details
explaining that limitation. No text is silently truncated. Total joined text is
bounded to 16 overlapping chunks, and a plan contains at most 64 unique checks
including standalone instruction checks. These bounds apply before enqueueing,
regardless of cache contents. Character overflow records `failed/input_too_large`;
plan overflow records `failed/plan_too_large`, without a detector call.
The separate per-attempt budget permits 16 actual detector calls. Cache hits,
including coalesced successful checks, do not spend this budget. Thus previously
checked instructions can support a plan with more than 16 checks. A cold or
invalidated cache may exhaust the call budget after partial work; this records
`failed/detector_budget_exceeded`, never a passed result or enforcement event.
Timeouts record `failed/timeout`. Any incomplete
check fail the job rather than representing partial work as passed. All checks
share the existing job timeout and execute serially. Moderation stays asynchronous.

Jev scores have a bounded process-local cache: 24-hour TTL, 4,096 entries with
least-recently-used eviction, and coalescing of concurrent identical checks.
Replicas do not share entries and restart clears them. Cache keys use a random
process HMAC over user/group scope, exact detector text, endpoint and credential,
model version, rule version, and the actual selected category rules. Prompts and
credentials are not retained in entries or logged. Existing encrypted queue
storage now also carries instruction text. Only validated successful scores are
cached; failures and missing categories are never cached. Settings are resolved
before every lookup, so configuration/key changes and resolution failures cannot
reuse stale configuration. Legacy Qwen results are not cached.

Thresholds and penalties are deliberately not cached: current policy and trust
are still checked before/after classification, and current thresholds are applied
to raw scores. A cache hit still produces a request-specific result and follows
existing incident deduplication. Safe results never create strikes. This feature
adds no model-name exemptions.

Deployment must account for queued tasks containing the new instructions field:
older workers ignore it. Do not mix old/new consumers when claiming this coverage;
a rollback does not retain the expanded coverage contract. Extraction/cache tests
are structural validation, not a real-provider accuracy evaluation.

## Moderation diagnostics and operational metrics

Migration 108 adds nullable `input_moderation_results.diagnostics`. It must be
applied before this version; older rows remain unknown rather than showing
invented counts. This is additive, with no Ent schema changes. Rollback keeps
the column and does not require a database restore.

Administrator usage details retain numeric metadata for successful, skipped and
failed jobs: new user characters, instruction characters/block count, planned
and completed checks, cache hits, detector calls, effective character/check/call
limits, processing milliseconds and queue wait milliseconds. `limit` identifies
`input_chars`, `planned_checks`, or `detector_calls` when applicable. Diagnostic
data and structured `input_moderation_result` logs contain no input text,
instruction bodies, detector responses, credentials or content fingerprints.
Request/source/turn correlation in logs does not require full-body capture.
Diagnostic persistence follows the existing terminal-result overwrite guard;
late pending/failure writes cannot replace a passed/flagged result.

Admin-only `GET /api/v1/admin/settings/jev` also exposes `runtime_metrics`.
The Gateway settings card displays outcomes by status/reason, completed checks,
cache hits, detector calls, cumulative processing time and queue wait, with a
manual refresh that preserves unsaved configuration. There is no polling or
detector probe. These counters measure local processing attempts including
retries, not unique requests, persisted outcomes or risk alerts. Pending enqueue
writes are excluded. They reset on process restart and are not shared across
replicas; aggregate structured logs externally for fleet/time-window analysis.
Do not claim historical failure percentages or site-wide latency percentiles
from this process-local snapshot. A failed detection is an operational failure,
not a user violation and never an additional strike.
