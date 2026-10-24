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

Stable instruction text is classified on its own and also joined with the new
user text for context assessment. Standalone instruction checks use bundles:
instruction blocks are packed in order into bundles of at most one chunk, and a
bundle closes after a block whose content hash selects a boundary (probability
proportional to block size, about one per quarter chunk). Claude Code sends
100+ small blocks per turn, so a turn needs a few bundle checks instead of one
check per block. An edit usually invalidates only nearby bundles' cached
scores: packing realigns at the next unchanged hash boundary, though a long run
without hash boundaries (closed by the size limit) can shift further. Blocks of
a full chunk or more stay standalone and are chunked like any text; a single
block beyond the 16-chunk bound fails the job as `input_too_large`. Requests
without new user text (tool-result turns) check only the bundles. A safe cached document cannot exempt a new
question. Content changes invalidate reuse. Small combined inputs use one joint
assessment; long inputs use overlapping Unicode-character chunks (default
32,768 characters, up to 512 characters overlap). Each category retains the maximum
score across all required checks, then applies the current group threshold once.
Cross-chunk semantics are limited to the overlap, not full-document equivalence.
Passed chunked results therefore display **Chunk checks passed**, with details
explaining that limitation. No text is silently truncated. Total joined text is
bounded to 16 overlapping chunks. When the instructions and the new text
together exceed that bound, the joint check is skipped instead of failing the
job: the new text is chunked alone (it must itself fit the bound) and the
instruction bundles are checked as usual. The result reason is
`joint_context_skipped` and the diagnostics carry `joint_skipped=true`; such
results do not represent a joint judgement of instructions and question. A plan
contains at most 64 unique checks including the instruction bundle checks.
These bounds apply before enqueueing, regardless of cache contents. Overflow of
the new text alone records `failed/input_too_large`; plan overflow records
`failed/plan_too_large`, without a detector call.
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

## Encrypted moderation evidence (schema 113)

Migration 113 adds `input_moderation_evidence`, `input_moderation_evidence_access`
and the nullable `input_moderation_events.incident_fingerprint` with a partial
index. It is an ordinary transactional migration (both tables are new and the
events table is small); apply it through the reviewed migration runbook before
this version.

**What is kept.** When a scored (Jev) job is flagged, the checks whose *own*
score met the final threshold (same audited categories, thresholds and `>=` as
the decision) are encrypted and stored with the event. Each check records its
kinds: `text` (new user text alone), `instructions` (an instruction bundle) or
`joint` (instructions and new text checked together — a check type, not a claim
about which part triggered), plus bundle number and character range. At most 4
checks are kept: first the best check for each enforcement category, then for
the other flagged categories, then further crossing checks by margin. Anything
left out sets `evidence_truncated`, `evidence_omitted_categories` and
`evidence_omitted_checks` in the result diagnostics. A check is the configured
chunk (default 32,768 characters). Legacy Qwen results have no per-check scores
and keep no evidence.

This table is the only persisted store of moderated text, besides the existing
encrypted task queue. Diagnostics and logs never contain text.

**Capture is best effort.** The flagged result's diagnostics carry
`evidence_status`: `stored`, `no_key` (`totp.encryption_key` not explicitly
configured), `encrypt_failed`, `unsupported` (no per-check scores),
`selection_error` (a flagged category had no crossing check; nothing else is
substituted), `disabled` (`evidence_retention_days: 0`) or `user_deleted`.
The event, the evidence and the flagged result row (and any strike transition)
commit in one transaction on both write paths; a redelivered job writes nothing.
Missing evidence is never backfilled.

**Encryption.** Keys are derived with HKDF-SHA256 from `totp.encryption_key`
(`saiai/moderation-evidence/enc/v1` and `.../mac/v1`); the TOTP/queue encryptor
format is unchanged. AES-256-GCM authenticates the text together with its scores
and categories; the additional data (job, user, group, check index, kinds) is
stored immutably per row and must match the event on decrypt. Each row records a
`key_id`. When rotating `totp.encryption_key`, list former keys in
`gateway.input_moderation.evidence_previous_keys` (decrypt only) until their
evidence has expired.

**Access.** `GET /api/v1/admin/usage/input-moderation-events/:id/evidence`
accepts only a personal administrator JWT; the shared admin API key gets 403.
The access row (admin, method, time) is committed before anything is decrypted,
so text is never returned without an audit record; responses are `no-store`.
Expired, unheld evidence is not returned. `POST .../evidence/hold`
(`days` 1–365, `reason` required) and `POST .../evidence/release` update the rows
and write an access row in one transaction. `GET
/api/v1/admin/usage/input-moderation-evidence?held=1` lists held evidence with
event metadata only, so it stays reachable when result rows are gone. The risk
alert list exposes `event_id`, `evidence_count` and `evidence_status` per event;
the details dialog offers **View original text** after a confirmation that the
view is recorded.

**Retention.** `gateway.input_moderation.evidence_retention_days` (default 90;
0 disables capture). An hourly cleanup, independent of ops settings and guarded
by a PostgreSQL advisory lock, deletes expired rows that are not held (the
locked row is re-checked, so a committed hold wins) and access rows older than
730 days. Soft-deleting a user deletes their unheld evidence in the same
transaction; evidence writers lock the user row and skip evidence for a deleted
user (an in-flight incident for a deleted user is recorded as `already_inactive`
without a strike). `SAIAI_HTTP_TRACE` never records response bodies of the
evidence endpoints. Rolling back to the previous binary keeps schema 113; the old binary
ignores the new tables and column, and evidence cleanup pauses until roll-forward.

## Repeated instruction bundles and strikes

Projects resend the same instruction bundles every turn. When an enforcement
category is crossed only by a stable bundle, each turn used to count a new
strike. Now an incident gets a fingerprint (HMAC with the evidence MAC key over
the rules version, the flagged enforcement categories and per-user content MACs
of the crossing instruction checks) only when:

1. every flagged enforcement category is crossed by an instruction-bundle check,
   and
2. the new user text alone crosses no enforcement category. If the plan has no
   text-only check, one extra text-alone check runs, only for incidents that
   would count toward auto-enforcement with non-empty new text. It uses the
   content cache and the job's detector budget; if it cannot complete, the
   incident has no fingerprint and counts.

Under the user lock the strike window is rolled over (or restarted after a
reset) first; then the existing duplicate checks and the fingerprint check only
consider strikes counted in the current window. A matching earlier strike makes
the incident `duplicate_ignored`. A new enforcement category (A → A+B) changes
the fingerprint and counts. Strike logic never reads the evidence table, so
evidence retention settings do not change strikes. Without an explicitly
configured key there is no fingerprint. Rotating the key changes the MACs, so a
repeated bundle may count once more after rotation.

Keep `auto_disable_user` off until every instance runs this release: older
instances neither write nor honour fingerprints.
