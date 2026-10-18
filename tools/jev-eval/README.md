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

A future single pass over this draft would require 76 requests with zero automatic
retries. No such requests are authorized or implemented by this tool. Before a
real run, present the final reviewed dataset, exact call budget, token/price-based
cost estimate and version hashes for approval. No monetary estimate is claimed
from character counts, and zero model calls have been made for this draft.

## Boundary candidate and review

`python3 tools/jev-eval/validate.py --suite` checks the original 76 samples plus
32 proposed boundary samples, for 108 examples / 54 bilingual scenario families.
It rejects shared IDs, family IDs and normalized text across splits, inconsistent
translation labels and duplicated labels. Semantic independence is not automated.
`suite-manifest.json` records both datasets' hashes. All samples remain unreviewed;
qualified independent holdout count and model call count remain zero.

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
