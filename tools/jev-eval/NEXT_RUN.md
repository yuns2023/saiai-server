# Expanded Jev evaluation — one approved run completed

## Concrete scope

- Model: `jev-1.13.0`; endpoint: `https://api.typesafe.ai/v1/systemone`.
- Dataset: 76 calibration drafts + 32 boundary candidates, 54 related bilingual
  families. All remain unreviewed; qualified independent holdout count is zero.
- One request per sample with all 19 registry questions: 108 requests maximum,
  serial execution, 15-second timeout, zero automatic retries.
- Only authored synthetic text and registry rules; no production traffic, user
  identifiers, live prompts, database mutations or production policy changes.
- The prepared packet/plan freezes request and source hashes. Rebuild and obtain
  a new scope decision if rules, model, samples or questions change.

## Status and prerequisites

The blind annotation packet generator and offline validation are implemented.
An explicit-opt-in network runner and offline result reporter are implemented.
Fourteen offline/mock tests passed before execution. One specifically approved
108-call batch has now completed; see [results](RESULTS_20261018.md). Its approval
is consumed and cannot be reused.
Price/byte illustration and its limitations are documented in README.md.
The completed run returned full input-token usage and a usage-based cost estimate.
There is still no enforced monetary cap; see the results for accounting limits.

A coordinator must obtain reviewer annotations, document author independence,
reconcile disagreements and inspect semantic family overlap before calling any
set an independent holdout. Alternatively, an explicitly approved exploratory
run can use these drafts, retaining their unreviewed status and reporting only
provisional agreement with authored labels. Neither route authorizes penalties.

The runner enforces the frozen 108-call limit, refuses redirects and retries,
validates model/scores and writes only redacted failures. It records reported input
tokens when available and otherwise marks usage incomplete. It does not enforce
a monetary cap; an absent usage field is never treated as zero cost. Present the
final dataset/runner hashes and the available budget controls for specific user
authorization. Any further run needs new specific authorization. Previously consumed
model-call approvals are not reusable.

Report per-category and protected-context results, unknown labels, request
failures, coverage, latency and reported usage. Flag incomplete usage/cost instead
of treating errors as free or safe. Retain family relationships when computing
uncertainty. Do not tune on a holdout and subsequently claim it was unseen.
