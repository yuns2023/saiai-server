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
