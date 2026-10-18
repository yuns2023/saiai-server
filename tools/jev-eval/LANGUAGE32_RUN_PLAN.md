# Proposed language-32 exploratory batch

Status: prepared and mock-tested, **not executed or authorized**. The previous
108-call approval is consumed and does not cover this batch.

## Frozen scope

- Profile: `language-32`; model: `jev-1.13.0`.
- Endpoint: `https://api.typesafe.ai/v1/systemone`.
- Exactly 32 authored synthetic inputs, 16 bilingual families / six related
  contrast groups; all 19 unchanged registry questions on each request.
- Maximum 32 calls, single concurrency, 15-second timeout, no automatic retries,
  no redirects and no replay/resume. Failures consume call slots.
- Report threshold fixed to 0.90; report category counts separately for zh/en.
  No model/rule/production-threshold changes or automatic-penalty activation.
- Fixture SHA-256: `ac26331b372805f673ffd489e0d7014fc496283b4af2bc199891c2eb2b5cafa5`.
- Plan SHA-256: `299d31695a831c693018ed48a78a02c3cf1948890c4927b7124adca782b6a6cd`.
- Runner SHA-256: `87035239305ea8653227ac59656fe09d6d7bbfb2c77319bc383cfa7c23126d4b`.
- Registry SHA-256: `b0ca7c8a54149a97a1743150ce3fe53429482d9c99da5c6bce4426545a6c954a`.

## Label review and interpretation

Author-side review checked all 32 texts against the unchanged category rules and
compared each Chinese/English pair. Proposed labels were retained. This does not
constitute independent annotation: the author saw the earlier model results.
The samples remain `unreviewed`, calibration-only. In particular, third-party
self-harm mentions are negative under the current **speaker-intent** definition;
this label is not a judgment about whether the third party needs support.

The purpose is to inspect language and wording sensitivity while holding rules,
model and threshold fixed. Report failed/missing calls, unknown labels and score
differences without changing labels to fit the model. Do not interpret 32 examples
as an enforcement-precision estimate or independent acceptance evidence.

## Budget illustration

Complete compact serialized requests total **268,916 UTF-8 bytes**.
Using the USD 0.042/M input-token rate checked on 2026-10-18 at the
[official model page](https://docs.typesafe.ai/models), the one-byte-as-one-token
illustration is **USD 0.01129447**.
This is not tokenization or a hard spending cap. The runner enforces 32 attempts,
not a monetary ceiling. Record actual returned input usage and mark incomplete
usage explicitly; errors may still incur charges. Output tokens are free under
the checked pricing. No Key is included in the packet, plan, source or report.

## Execution after separate authorization

Prepare with `prepare_review.py --profile language-32 --output NEW_DIRECTORY`.
Validate using `run.py --plan NEW_DIRECTORY/request-plan.json` (no network/key read).
Only after explicit approval, use that plan with `--execute --output NEW_RESULTS`
and inject `JEV_EVAL_API_KEY` into the process environment. Do not alter a 108-call
plan by hand: complete-plan comparison and profile hashes reject such substitution.
Results carry the profile and fixture hashes; the reporter rejects mixed datasets.
The old 108-call results remain readable with their original aggregate counts.

Validation: 19 offline/mock tests passed, including exactly 32 mocked requests,
profile/budget tampering rejection before network, language slices and original
108-call report compatibility. No additional provider request has been made.
