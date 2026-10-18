# Language-32 exploratory results — 2026-10-18

One specifically approved batch completed: **32/32 requests succeeded**, zero
retries, fixed `jev-1.13.0`, all 19 unchanged rules and threshold 0.90. The approval
is consumed; no rerun or further calls are authorized. Production configuration,
global threshold, enforcement eligibility and Qwen remain unchanged.

## Evidence and scope

- Source: `0af14225d` (the frozen implementation before this report).
- Runner SHA-256: `87035239305ea8653227ac59656fe09d6d7bbfb2c77319bc383cfa7c23126d4b`.
- Fixture SHA-256: `ac26331b372805f673ffd489e0d7014fc496283b4af2bc199891c2eb2b5cafa5`.
- Registry SHA-256: `b0ca7c8a54149a97a1743150ce3fe53429482d9c99da5c6bce4426545a6c954a`.
- Results SHA-256: `b959acd20e3fa99f7b412bc5209867816204b97ea4c1e8c79c5daf8c8d2c235a`.
- Frozen plan and result metadata are retained locally with the consumed approval.
- 32 synthetic samples / 16 bilingual families / six related contrast groups.
  All labels remain authored and unreviewed. These are observed calibration data,
  not independent acceptance trials. No fixture labels were changed to fit scores.

## Agreement with authored partial labels

| Language | TP | TN | FP | FN |
| --- | ---: | ---: | ---: | ---: |
| zh | 12 | 36 | 0 | 0 |
| en | 11 | 36 | 0 | 1 |
| Total | 23 | 72 | 0 | 1 |

Counts cover 96 explicitly labeled category/sample judgments, not 32 independent
accuracy trials. The remaining 512 judgments are unknown and excluded. Zero FP
in these labeled negatives is not proof of a production precision target.

- Harassment: 4 positive and 4 negative examples per language matched the draft
  labels. The positive scores were 0.98–0.99. These are new explicit/indirect/
  polite/fictional-framing examples; the original missed English request was not
  rerun. Its failure must not be declared fixed from these different examples.
- Self-harm intent: Chinese 4/4 positive detections, English 3/4. The English
  colloquial support request scored **0.89**, while its Chinese pair scored 0.95.
  This repeats a near-threshold observation from the first batch; it does not
  establish that a lower threshold is generally safe.
- All 16 self-harm/support/recovery/quotation examples stayed below 0.90 for
  Unethical Acts, self-harm encouragement and self-harm instructions. Current
  personal intent is a support signal, not misconduct.
- The 16 negative-target inputs (harassment quotation/victim/criticism/prevention
  and self-harm past/third-party/quotation/ordinary distress) had no category at
  or above 0.90. This additional score inspection does not assign new ground truth.

## Usage and latency

- Complete returned usage: **57,606 input tokens**.
- At the USD 0.042/M input-token rate checked on 2026-10-18 at the
  [official model page](https://docs.typesafe.ai/models), calculated usage cost is
  **USD 0.002419452**. This is not an invoice reconciliation.
- Median end-to-end latency: **612.06 ms**; nearest-rank p95: **737.36 ms**.
- Zero observed request failures in this batch does not establish production SLA.

## Decision

Keep the existing 0.90 threshold and new-category audit-only restriction. Do not
automatically punish self-harm intent/help-seeking or broaden old-category rules.
No rule/threshold was tuned on this batch and no extra calls were sent. The two
completed exploratory batches are sufficient to identify wording sensitivity,
not to claim independent effectiveness. Before enforcement promotion, obtain
independent annotation and fresh acceptance families. A production audit-only
rollout, if requested, remains a separate release-readiness and authorization task.
