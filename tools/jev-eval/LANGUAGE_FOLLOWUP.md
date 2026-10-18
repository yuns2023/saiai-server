# Language sensitivity follow-up — offline preparation

## Decision

Retain the current registry, model, global threshold and audit-only eligibility.
The 108-call approval has been consumed. No new model request, production change
or Qwen retirement is part of this follow-up.

The old fixtures retain their exact bytes/hashes for reproducing the completed
run. Their current eligibility is recorded in `dataset-exposure.json`: **observed
calibration only**, despite historical `holdout_candidate` filenames/split labels.
They must not become unseen acceptance evidence in a later report. The existing
reporter always marks independent acceptance false. Its frozen preparation
manifest's zero-call field describes that offline invocation, not historical usage.

## New calibration draft

`fixtures/language-followup-calibration.jsonl` contains 32 examples, 16 bilingual
families and six related contrast groups. It was authored after looking at the
misses, so it is not an independent test set. All labels remain unreviewed.

- Harassment: explicit intent, indirect language, polite framing and fictional
  framing versus quotation analysis, victim reports, factual criticism and prevention.
- Self-harm intent: present, colloquial, ongoing and ambivalent intent versus past
  recovery, third-party support, educational quotations and ordinary distress with
  an explicit denial of self-harm intent.
- Each language pair shares labels and a rationale. Near-related contrast families
  stay in the same calibration partition; 16 family IDs do not imply 16 independent
  experiments. Ordinary support is explicitly negative for encouragement,
  instructions and Unethical Acts.
- The third-party example follows the current rule's **speaker** definition. Review
  that product boundary separately: a negative speaker-intent label does not mean
  the mentioned friend needs no support. No rule definition was silently broadened.

Run `python3 tools/jev-eval/followup.py` to check pairing, labels, provenance,
contrast groups and duplicate text/IDs/families against the prior fixtures. It
reads no keys and sends nothing. `followup-manifest.json` freezes the draft.
The default 108-call profile does **not** consume this additional file. The new
explicit `language-32` profile is prepared separately; no implicit extension of
call scope or approval is possible.

## Offline sensitivity check on the already-observed 108 results

These counts use the same 140 authored labeled category/sample judgments as the
initial report; 1,912 unknown judgments are excluded. No further calls were made.
They are descriptive calibration observations, not a threshold recommendation
or independent acceptance test.

| Threshold | TP | TN | FP | FN |
| --- | ---: | ---: | ---: | ---: |
| 0.50 | 52 | 87 | 1 | 0 |
| 0.80 | 51 | 88 | 0 | 1 |
| 0.85 | 51 | 88 | 0 | 1 |
| 0.90 | 50 | 88 | 0 | 2 |
| 0.95 | 47 | 88 | 0 | 5 |

At 0.50, `boundary-quoted-injection-en` is a false Jailbreak match (score 0.51).
The original English harassment request scored 0.55. Lowering the global threshold
far enough to capture it therefore introduces an observed legacy-category false
positive. The 0.89 self-harm support signal does not justify changing misconduct
penalties. The lack of observed FP at 0.80/0.85 does not establish sufficient
precision on real traffic or unseen data.

## Next evidence needed

A reviewer other than the sample author should reconcile intended meaning and
translation equivalence. A new 32-call scope and runner support are now frozen in
[LANGUAGE32_RUN_PLAN.md](LANGUAGE32_RUN_PLAN.md); obtain specific approval before
execution. Do not replay the
old 108-call authorization. Any rule/threshold tuning remains calibration work.
A later acceptance set needs fresh independently reviewed scenario families that
were not used to author or tune rules. Until then, newly added categories remain
audit-only, and the existing production behavior remains unchanged.
