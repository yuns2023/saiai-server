# Jev input moderation

The default engine is `jev`, pinned to `jev-1.13.0` and the bundled four-category
v2 rules. Only the latest real user text is submitted, without site identity.
There is no automatic detector fallback. The old `qwen` adapter requires an
explicit engine setting and is retained for controlled rollback only.

Set `GATEWAY_INPUT_MODERATION_API_KEY` using deployment secret management.
Leave `gateway.input_moderation.endpoint` empty for the official TypeSafe URL;
remove any previous local-sidecar endpoint override. Never put a credential in
version control. HTTPS is required, redirects are rejected, response size is
bounded, and model/version/score validation must succeed before enforcement.
A missing key disables submissions. Errors do not generate strikes.

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

Admin usage records expose matching category scores, the decision-time threshold,
action, and strike-count flag. A durable metadata link maps the local request and
WS turn to the final usage billing ID; identity joins include user, API key, and
group. Either the classification or the usage record can arrive first. The admin
usage page refreshes its list every 30 seconds while visible. Absence of a hit is
not represented as a successful safety check. Failed forwarding requests without
a usage row can still have moderation events. Raw text is never written to the
event or link tables. Existing encrypted queue retention behavior is unchanged.

Migration 100 adds the threshold, event score metadata, and correlation table.
Deploy code and migration together. This development change does not switch a
running deployment, remove a sidecar, send a provider evaluation, or change
production credentials. Old rows have no fabricated score. Event/link retention
and a dedicated errors/pending dashboard remain follow-up work.

## Local validation

Targeted Go tests (serial, `GOMAXPROCS=2`, including the `unit` build tag) cover
Jev mock TLS responses, strict response validation, threshold boundaries and
policy reload, optional disable/cooldown, duplicate handling, usage identity
correlation, and DTO mappings. Frontend typecheck, targeted ESLint, and seven
UsageTable tests cover asynchronously arriving labels and scoreless legacy rows.
No live provider calls, production database migration, or full CI matrix ran.
