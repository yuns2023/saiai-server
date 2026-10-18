# Proposed expanded Jev evaluation — not authorized

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
A network runner is not implemented in this work. Six offline tests pass.
Price/byte illustration and its limitations are documented in README.md.
Actual tokenizer-based cost and an enforced monetary cap remain unconfirmed.

A coordinator must obtain reviewer annotations, document author independence,
reconcile disagreements and inspect semantic family overlap before calling any
set an independent holdout. Alternatively, an explicitly approved exploratory
run can use these drafts, retaining their unreviewed status and reporting only
provisional agreement with authored labels. Neither route authorizes penalties.

Before a live run, implement and mock-test exact call limits, no redirects/retries,
strict model/score validation, redacted errors and usage reporting. Present the
final dataset/runner hashes and the available budget controls for specific user
authorization. The current broad request to continue preparation is not approval
for paid calls. Previously consumed model-call approvals are not reusable.

Report per-category and protected-context results, unknown labels, request
failures, coverage, latency and reported usage. Flag incomplete usage/cost instead
of treating errors as free or safe. Retain family relationships when computing
uncertainty. Do not tune on a holdout and subsequently claim it was unseen.
