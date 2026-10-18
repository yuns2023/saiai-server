# Versioned Jev policies (development)

This change depends on the result/configuration work in PR #63 and migration 103.
Migration 104 adds an optional group policy and metadata-only decision snapshots
on moderation events/results. It does not opt existing groups into new categories.

## Registry and compatibility

`backend/internal/service/jev_category_registry.json` is the shared source for
category IDs, Chinese/English labels, boundaries, rules and enforcement eligibility.
`GET /api/v1/admin/settings/jev/categories` returns the admin-only metadata catalog;
the frontend no longer maintains the obsolete nine-option list.

The four original category IDs and rule text remain unchanged. A frozen legacy
fixture verifies that compatibility. Nineteen categories are available in the
expanded registry: the four existing categories plus harassment (2), hate (2),
violent wrongdoing (1), violence (2), sexual content (3), self-harm (3), harmful
political conduct (1), and explicit unauthorized copyright use (1).

These are authored detection rules, not proven accuracy or legal conclusions.
All fifteen new categories are locked to audit-only in the backend as well as the
UI until independently evaluated. Existing four-category enforcement remains
available. A multi-label input may still trigger an existing selected category;
new audit-only labels never independently contribute a strike.

## Group API

`input_moderation_policy` is optional in group create/update/read payloads:

```json
{
  "rules_version": "jev-expanded-v1",
  "audit_categories": ["PII", "violence", "self-harm/intent"],
  "enforcement_categories": ["PII"],
  "thresholds": {"PII": 95, "violence": 90}
}
```

- NULL/omitted stored policy preserves the existing four-category behavior.
- A missing/null policy in an update leaves the stored policy unchanged.
- `jev-v2` is an explicit policy restricted to the four existing categories;
  `jev-expanded-v1` permits selecting the new categories.
- Empty explicit enforcement means **none**. Empty legacy category selection
  retains the preexisting all-category meaning; the editor represents that as
  checked legacy categories and emits an explicit policy on edits.
- Empty audit selection produces a skipped result and sends no model request.
- Enforcement must be a subset of audit and pass registry eligibility validation.
  Unknown categories, duplicates, unsupported versions and out-of-range thresholds
  are rejected. Existing unsupported legacy selections are preserved on unrelated
  edits but cannot be newly introduced.
- Missing category thresholds inherit the group's existing 1–100 threshold.
- Opting into expanded detection does not enable penalties for new categories or
  change the group's global automatic-action switch.

The worker reads group policy before classification and again afterward. A changed
rules version or detection set yields `skipped/policy_changed`; changed thresholds
and enforcement selections are applied from the fresh policy. Explicit policies
require Jev and complete scores for all selected questions; there is no Qwen fallback.
The client sends only selected rules and continues strict model/score validation.

Successful result/event snapshots include the effective rule version, detection
and enforcement sets, resolved category thresholds, group update time, automatic
handling flag, action, cooldown, strike threshold/window and dedupe duration.
Snapshots contain no user input. The usage UI shows per-category thresholds and
rule version for these results. Historical rows keep their historical labels.

## Validation and release boundary

Local TLS mock tests verify selected questions and reject missing scores. Unit
and PostgreSQL/Redis integration tests cover audit-only enforcement, empty selection,
thresholds, in-flight policy changes, group/auth mapping, snapshot persistence,
identity/turn isolation and existing concurrent strike deduplication/reset behavior.
Frontend tests cover opt-in, unchanged legacy load, locked penalties and empty
explicit enforcement. Offline preparation is in `tools/jev-eval/README.md`.

This is development work: it does not make new-category effectiveness claims,
run paid evaluations, enable production policies or remove Qwen. The 76-example
bilingual draft spans 38 families, has no independent review and is not a holdout.

Deploy with migration 104 only through the reviewed release process. Once groups
use explicit policies, an application rollback must target a version that understands
those policies. Do not blindly roll back to the pre-policy application: it reads
legacy fields and could restore a different detection/penalty selection. Any policy
conversion for an older rollback requires an explicit reviewed plan in private Ops.
Result retention/queue operations and final Qwen service removal remain subsequent
operational work; no service or historical records are removed by this migration.
