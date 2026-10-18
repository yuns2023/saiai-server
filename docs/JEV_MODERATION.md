# Jev input moderation

The default engine is `jev`, pinned to `jev-1.13.0` and the bundled four-category
v2 rules. Only the latest real user text is submitted, without site identity.
There is no automatic detector fallback. The old `qwen` adapter requires an
explicit engine setting and is retained as a legacy adapter. It is not a verified
rollback procedure: scoreless Qwen results cannot enforce the numeric threshold.
Removing the running Qwen service is a separate production operation.

Set `GATEWAY_INPUT_MODERATION_API_KEY` using deployment secret management.
Leave `gateway.input_moderation.endpoint` empty for the official TypeSafe URL;
remove any previous local-sidecar endpoint override. Never put a credential in
version control. HTTPS is required, redirects are rejected, response size is
bounded, and model/version/score validation must succeed before enforcement.
A missing key records a failed moderation job. Errors do not generate strikes.
Admin configuration and result states are described in [INPUT_MODERATION.md](INPUT_MODERATION.md).
The planned category expansion and Qwen retirement are tracked in [JEV_EXPANSION_PLAN.md](JEV_EXPANSION_PLAN.md).

Group moderation and automatic enforcement remain explicit opt-ins. New groups
use a 90% score threshold, three strikes in a 24-hour fixed window starting with
the first strike, and a 30-minute cooldown. A score is a detector output, not a
measured 90% probability of guilt. Classification policy is reloaded after
inference so changes made during a slow request take effect.

`warn_then_cooldown` records warnings before the threshold and applies a temporary
cooldown at/after it. `warn_then_disable` optionally disables the site user at
the threshold. Legacy `cooldown_then_disable` and `immediate_disable` are
preserved for existing explicitly configured groups; migration does not silently
replace their action settings. Administrators remain exempt. Existing groups
receive the new 90% numeric threshold; scoreless legacy results cannot trigger
numeric-threshold penalties. A group with enforcement off still records hits.

An incident counts once regardless of how many categories match. Job retries and
repeated events for the same authenticated request/source/turn are deduplicated;
identical text is also deduplicated within the configured content window.
The strike state is user-global across groups. Admin risk reset/unblock controls
remain the existing ones. Warnings are audit/UI actions, not email or Slack.

The admin usage API exposes matching category scores, the decision-time threshold,
action, and strike-count flag. The table renders categories, scores, action and whether the incident counted;
new result metadata also exposes passed/skipped/failed/pending states and the evaluated threshold.
The local request/source/turn reference is stored atomically in the usage row,
including batched, best-effort and retry insert paths. Classification and usage
may arrive in either order. The join additionally requires the authenticated
user, API key and group. Correlation metadata follows usage retention; there is
no separate link table or orphan-link cleanup job. A failed usage write follows
the existing usage retry behavior without making billing depend on moderation.
An enrichment query has a one-second deadline; failure clears partial labels
and returns the usage page with explicit unavailable metadata.
The admin
usage page refreshes its list every 30 seconds while visible. Absence of a hit is
not represented as a successful safety check. Failed forwarding requests without
a usage row can still have moderation events. Raw text is never written to the
event or link tables. Existing encrypted queue retention behavior is unchanged.

Migration 100 adds the threshold, event score metadata, and usage correlation column.
Deploy code and migration together. This development change does not switch a
running deployment, remove a sidecar, send a provider evaluation, or change
production credentials. Old rows have no fabricated score. Usage-row status is implemented separately by migration 103; a dedicated queue operations dashboard remains follow-up work.

## Local validation

Targeted Go tests (serial, `GOMAXPROCS=2`, including the `unit` build tag) cover
Jev mock TLS responses, strict response validation, threshold boundaries and
policy reload, optional disable/cooldown, duplicate handling, usage identity
correlation, and DTO mappings. Frontend typecheck, targeted ESLint, and seven
UsageTable tests cover asynchronously arriving labels and scoreless legacy rows.
Disposable PostgreSQL migration and integration checks cover single/batched/best-effort
usage correlation, both arrival orders, turn isolation, duplicate inserts, concurrent
incident deduplication, threshold escalation and reset races. Concurrent incident
inserts exposed a foreign-key lock upgrade deadlock; the user lock now uses
`FOR NO KEY UPDATE`, which serializes decisions without conflicting with the
event foreign-key check. No live provider calls or production database migration ran.

## Follow-up before production consideration

- Audit existing group policies during release preparation: migration preserves
  old action modes and hit limits. New defaults do not convert existing groups
  from first-hit cooldown/second-hit disable to the new three-hit policy.
- Complete independently reviewed evaluation before claiming safety effectiveness.
  The 80-case calibration aggregate has zero false positives/negatives, but PII
  and crime each miss one category label, while crime and unethical acts each
  produce two extra category labels. Aggregate accuracy is not category accuracy.
  The 700-case draft has only 140 scenario families and has not been run.

User authorization selected Jev as the development default; it did not establish
holdout qualification or authorize deployment. Use `saiai-release` and private Ops
for any future rollout. Do not rerun paid evaluations based solely on old consent.

Explicit opt-in category expansion is described in [JEV_EXPANDED_POLICY.md](JEV_EXPANDED_POLICY.md); NULL group policy continues to use the four-category rules described above.
