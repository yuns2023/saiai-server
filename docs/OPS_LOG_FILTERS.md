# Ops log filtering

Administrators manage detailed error-log suppression through **Ops Monitoring →
Log filtering**. An error detail also offers **Filter similar errors**, which
creates a disabled draft from that record. Confirm the source and scope before
enabling it. Rules never delete existing records or modify client responses,
authentication, billing, rate-limit handling, retries, or account failover.

## Friendly defaults

- Common presets cover locally rejected inactive users and invalid API keys.
  They remain disabled until an administrator explicitly confirms them.
  **Confirm and enable** saves a new rule immediately; it does not require a
  second Save action. Existing rule toggles and removals require Save.
- Error-derived drafts retain the known group and platform. A rule without
  either scope requires explicit global confirmation in the editor.
- Disabling a rule resumes subsequent detail recording. Suppressed details
  cannot be recovered. Normal access/security logs remain independent.
- Counters count suppressed request-error records, not upstream attempts, and
  are **per process since startup**. They reset on restart and are not durable
  or cluster-wide totals. No request content or credentials enter the counters.

## Source-aware matching

Native and Google API-key authentication explicitly mark missing keys, invalid
keys, and inactive users in server-owned request context. A local rule requires
one of these reasons, not merely HTTP 401, a response code, or matching text.
Missing user records and authentication database failures are not marked.
Missing/invalid key rules cannot use a group scope: authentication has not yet
established a group. Use platform scope, or explicitly confirm a global scope.
Incomplete auth snapshots retain the previous wire error but are not marked as
ordinary invalid keys; unknown user states and mismatched identities also remain
unmarked and recorded.

When recording a marked rejection, the existing error-type field stores
`gateway_auth_user_inactive`, `gateway_auth_invalid_api_key`, or
`gateway_auth_api_key_required`, with phase `auth` and source `client_request`.
This provides provenance for future drafts without a schema migration or a
change to the wire response. Unrecognized upstream-provided types cannot create
this provenance through the response parser.

Older records may suggest a local preset from a known response code when no
upstream evidence exists. Such suggestions are explicitly unverified: confirming
one only filters **future server-marked** local rejections. It never treats the
old response text as trusted evidence for runtime filtering.

Advanced upstream rules require both an HTTP status and a non-empty keyword,
joined with AND. Each list matches any of its entries: one listed status and one
listed keyword must match. Source, platform and group restrictions always apply. A record
containing multiple upstream attempts is suppressed only when every attempt
matches the same enabled rule. A matched harmless attempt cannot hide an
unmatched provider failure. Distinct terminal upstream context must also match;
an error response without a confirmed terminal status matching the client status
is retained. The final client error message must also match that same rule, so a
stale matching upstream context cannot hide a different final failure. Verified
upstream drafts pair their status and message from the same upstream outcome,
not a different client error. Existing error-passthrough rules remain independent;
this editor does not change response-rewrite behavior.

## Storage and operation

Rules use the independent `ops_log_filters` JSON value in the existing settings
table. No migration is required. The admin API provides:

- `GET /api/v1/admin/ops/log-filters`
- `PUT /api/v1/admin/ops/log-filters` with `rules` and the loaded `revision`
- `GET /api/v1/admin/ops/errors/:id/log-filter-proposal`

Stale editor revisions return 409 rather than silently overwriting a newer
configuration. Writes are serialized within the current process; this is not a
distributed compare-and-swap transaction across instances. Rule changes on
another instance are observed through a two-second cache. Rule-store failures
retain error recording instead of applying stale suppression. A busy filter-state
lock also retains recording rather than blocking requests behind settings I/O.
Rules, keywords, status-code lists, and counter state are bounded. The existing
advanced error-filter switches retain their settings. The invalid/missing API-key switch
now requires a local authentication marker, including Google-shaped errors;
upstream key failures with the same text remain recorded.

Validate with mocked native/Google auth, source-spoofing and upstream-401
regressions, mixed-attempt and recovered-error cases, editor revision conflicts,
scope confirmation, and frontend component tests. Do not use live model traffic
or delete historical logs as a smoke test.
