# Expanded Jev offline evaluation preparation

Run `python3 tools/jev-eval/validate.py` from the repository root. This tool has no
network client, never reads credentials, and never submits prompts.

The initial draft contains 76 bilingual examples across 38 scenario families and
19 categories. English/Chinese versions share a family and are not independent
trials. Every category has one positive and one negative family. They are authored
calibration drafts, not reviewed ground truth and not an independent holdout set.
Only listed positive/negative labels are asserted; unlisted categories are unknown,
not implicitly negative. Self-harm intent is a support signal, not a penalty label.

The draft is deliberately small and insufficient for validating deployment
precision/recall. Expand and independently review realistic boundary cases before
freezing a candidate. Keep calibration and holdout families disjoint; the earlier
700-case draft is not automatically imported or represented as reviewed evidence.
The manifest identifies the exact rules and fixture bytes; changes require a new
manifest and invalidate comparisons to prior frozen candidates.

The original 76-example draft requires 76 requests for a single pass; the combined
suite requires 108. The preparation tool itself sends none. Before any new real
run, present the exact dataset, call budget, token/price-based cost estimate and
version hashes for approval. No monetary estimate is claimed
from character counts, and no calls are made by the preparation tool. A later explicitly authorized
108-call exploratory run is recorded in [RESULTS_20261018.md](RESULTS_20261018.md).

## Boundary candidate and review

`python3 tools/jev-eval/validate.py --suite` checks the original 76 samples plus
32 proposed boundary samples, for 108 examples / 54 bilingual scenario families.
It rejects shared IDs, family IDs and normalized text across splits, inconsistent
translation labels and duplicated labels. Semantic independence is not automated.
`suite-manifest.json` records both datasets' hashes. All samples remain unreviewed;
qualified independent holdout count remains zero. The validator itself makes zero
model calls; that field is not a historical count of separately authorized runs.

See [REVIEW.md](REVIEW.md) for the author-side review, independent annotation
procedure and proposed execution/reporting contract. No model runner is included.

## Prepare a blind review packet

Run `python3 tools/jev-eval/prepare_review.py --output /path/to/new-directory`.
The command refuses an existing output directory and performs no network I/O.
Send `annotations.csv` and `category-definitions.json` to a reviewer other than
the author; keep `coordinator-map.json` with the coordinator. The CSV uses opaque
sample IDs and omits the author's proposed labels, rationales and split names.
Do not send the source fixture files as part of blind annotation. The public
repository already exposes those fixtures, so document whether the reviewer saw
them; this packet alone cannot establish blinding or reviewer independence.

For each category, enter `positive`, `negative`, or `unknown`, a rationale and
reviewer identity. Do not automatically copy author labels or interpret blank
cells as negative. Review translations as related examples. Returned annotations
need reconciliation and semantic family review before creating a qualified
holdout; the tool does not mark any row reviewed or promote any category.

The packet contains 108 samples x 19 categories = 2,052 annotation cells. The
request plan proposes all 19 questions on each of 108 serial calls, no automatic
retries, 15-second request timeout, fixed `jev-1.13.0`. It records payload hashes
and tool/dataset/registry hashes, without sending anything or reading credentials.

As checked on 2026-10-18, the [official model page](https://docs.typesafe.ai/models)
lists Jev 1.13 at USD 0.042 per million input tokens, with output free. The complete
compact request bodies total 903,652 UTF-8 bytes, including repeated questions.
**Only as an illustration**, assuming one input token per serialized byte gives
USD 0.03795338. This is not actual tokenization, a provider quote, a spending cap,
or approval. Python JSON bytes are a reproducible sizing format, not a claim that
the Go runtime emits byte-identical JSON. The current tool has no network runner
and cannot enforce a monetary cap. Confirm actual accounting and provider-side
budget enforcement before promising a hard cap; failed calls may incur charges.

The price check does not authorize a model upgrade: `jev-1.13.0` stays fixed even
if a newer preview exists. This draft may support a separately approved exploratory
run; it cannot establish independent acceptance or unlock automatic penalties.

## Execute only after specific authorization

`run.py --plan /path/to/packet/request-plan.json` defaults to offline validation:
no credential read and no network request. Add `--execute --output /new/results.json`
only after obtaining specific authorization for the frozen 108-call exploratory
scope. The CLI then reads `JEV_EVAL_API_KEY` from the process environment; never
put the key in arguments, output files or version control.

The runner compares the entire plan with current preparation, checks each request
hash, uses the fixed HTTPS endpoint/model, serial 15-second calls and no retries or
redirects. An existing results file is rejected before any request. Each attempted
slot is persisted before sending; there is no resume/replay mode. A crash requires
reconciliation and separately authorized follow-up, not a fresh automatic rerun.
Failed calls consume the 108-call budget. The runner enforces a call count, **not a
monetary cap**. Reported input token usage is retained when available; missing usage
is explicitly incomplete. No provider bodies, inputs, keys or exception strings
are written to results. Mock transports are labeled `mock`, never `provider`.

`report.py --results /path/to/results.json --threshold 0.9` validates fixture/rule
hashes, pinned model, unique sample IDs and all 19 finite scores before reporting.
Only explicitly labeled category/sample pairs enter TP/TN/FP/FN. Failed/missing
samples have separate counters and reduce positive detection coverage. Precision,
recall and false-positive rate on completed requests are explicitly conditional;
unknown labels are not negatives. Translations are counted as related families.
No independent confidence interval, acceptance claim or enforcement eligibility is
produced from these unreviewed drafts. The reporter does not calculate billing.

Current validation: 19 offline/mock tests, including an end-to-end 108-response
simulation, failure/redaction/no-retry behavior, altered-plan rejection, overwrite
refusal, redirect refusal and partial-label/error reporting. These simulations
send no network traffic and demonstrate no Jev accuracy.

## Follow-up after the first real run

[LANGUAGE_FOLLOWUP.md](LANGUAGE_FOLLOWUP.md) records a 32-example language calibration
draft and offline threshold sensitivity analysis of the existing results. Run
`python3 tools/jev-eval/followup.py` to validate it. It is separate from the frozen
108-call runner and not authorized for live execution. `dataset-exposure.json`
records that the original candidate fixtures are now observed calibration data;
their historical names do not make them eligible for independent acceptance.


The separately prepared `language-32` profile now supports exactly 32 calls and
language-specific reports. See [LANGUAGE32_RUN_PLAN.md](LANGUAGE32_RUN_PLAN.md)
for the frozen scope and budget illustration. It requires its own specific
approval; the default `expanded-108` profile is unchanged in sample content.
Use `prepare_review.py --profile language-32` to create the new packet. Earlier
plans are immutable historical artifacts; changed preparation hashes invalidate
them for execution but do not prevent offline reading of prior results.


The separately approved language-32 batch has now completed once; see
[LANGUAGE32_RESULTS_20261018.md](LANGUAGE32_RESULTS_20261018.md). Both batch approvals
are consumed. Offline validator fields describe tool behavior, not fresh authority
to rerun. No rule, threshold or production behavior changed after either batch.
