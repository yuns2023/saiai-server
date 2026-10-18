# Offline review and evaluation gate

## Review scope

The original 76 examples were reread against all 19 registry definitions. Their
listed labels are consistent with those definitions; this is an author-side
consistency review, not independent annotation or evidence of model accuracy.
Unlisted labels remain unknown. The original examples are mostly explicit,
short instructions; they are insufficient to establish behavior on realistic
ambiguous inputs, long context, code, or multilingual mixtures.

The 32 new examples (16 bilingual families) probe quoted injection, authorized
processing, defensive incident response, ordinary political criticism, news,
counter-speech, first aid, puberty education, victim support, self-harm support,
authorized reprinting, uncertain rights, deceptive fictional framing, classifier
injection, overlapping legacy labels, and changes to the user's own instructions.
Each includes a proposed label rationale. Self-harm support intentionally has a
positive intent label and negative encouragement/instructions labels.

These are `holdout_candidate`, NOT a qualified independent holdout. They were
written with access to the rules and calibration examples. Family IDs and text
are disjoint, but semantic similarity still requires human review; automatic
leakage checks cannot certify independence. A separate reviewer must either
accept a family with documented provenance or move it to calibration and obtain
new unseen cases. Do not tune rules/thresholds on these candidates and later
report them as unseen acceptance data.

## Independent annotation procedure

Before any provider call, a reviewer other than the fixture author should:

1. Review Chinese/English equivalence and operative intent without model scores.
2. Label each category as positive, negative, or unknown; preserve uncertainty.
   Resolve disagreements explicitly, recording a reason rather than forcing labels.
3. Review semantic family overlap across splits. Translations/paraphrases count
   as one family for statistical uncertainty, never as independent trials.
4. Include actual boundary content (with synthetic/private-data-free inputs),
   long inputs, code and mixed language. Current meta-requests alone are not enough.
5. Freeze separate calibration and unseen acceptance sets with reviewer evidence,
   dataset/rules/tool hashes, model version and per-category thresholds.

The current validator deliberately rejects claims of independent review and
qualified holdout status. A reviewed-data schema and evidence validation must be
added when actual reviewer records exist; changing the status string is not review.

## Proposed execution and reporting contract

No runner, credentials or network access is included here. The present drafts
contain 108 requests for one pass, with zero automatic retries. This is a size
estimate, not an approved execution budget. The final reviewed dataset may differ.
Before execution, provide the exact approved endpoint/model, question set, hashes,
call cap, timeout, concurrency, input and question token accounting, current pricing
and a monetary cap. Stop at either cap; any rerun needs an explicit new budget.

Freeze thresholds on calibration only. On acceptance, report each category's
TP/TN/FP/FN, unknown-label count, precision/recall/false-positive rate and uncertainty
at the family level. Report language and protected-context slices separately.
Transport errors, absent scores and model mismatch must be counted as failures,
not safe outcomes or discarded trials. Measure latency and completion coverage.
Do not interpret raw Jev scores as calibrated probabilities.

The proposed enforcement targets remain precision >=99% and recall >=95%, with
adequate independent families and uncertainty bounds. These tiny drafts cannot
support those claims. A protected-context false penalty blocks promotion pending
review. No additional category is eligible for automatic action from this work.
Self-harm intent remains a support signal rather than misconduct. Evaluate legacy
category overlap too: audit-only new labels do not prevent existing categories
from independently matching the same input.

## Code acceptance review

Reviewed settings encryption and redaction, endpoint replacement/redirect behavior,
selected-question construction and complete-score validation, legacy/null/empty
policy semantics, before/after-inference policy reads, enforcement eligibility,
metadata snapshots and result upsert isolation. No new blocking issue was found
in that scoped review; it does not replace independent review or production checks.

Existing limitations remain explicit: group policy reads are not a transactional
lock against a concurrent admin edit at the exact enforcement instant; result
retention needs an operational policy; old application versions do not understand
explicit group policies and are not safe blind rollback targets. Deployment must
resolve these operational constraints in its own reviewed plan.

## Subsequent authorized exploratory run

The planned single pass was later explicitly approved and completed. See
[RESULTS_20261018.md](RESULTS_20261018.md). This consumes that approval and exposes
the candidate set to model results; it is not an independent unseen holdout.
