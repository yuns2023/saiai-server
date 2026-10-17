# Upstream features worth evaluating

Reviewed the public Wei-Shaw/sub2api checkout at
[`b0a02a3304a0a78cdd003ddda1dd23ba02c03084`](https://github.com/Wei-Shaw/sub2api/tree/b0a02a3304a0a78cdd003ddda1dd23ba02c03084)
on 2026-10-18, against SAIAI Server base `4f1f84ad0`. These are recommendations,
not claims that upstream code can be merged without adaptation.

| Priority | Capability and source | SAIAI fit / proposed action |
| --- | --- | --- |
| Now | [Moderation score and threshold snapshots](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/content_moderation.go) | Adopt the auditability principle in the Jev work: score, decision threshold and action on usage records. Keep existing encrypted queue and metadata-only persistence. |
| Next | Same module: queue statistics, separate hit/non-hit retention, unban and flagged-hash removal | Existing SAIAI risk reset covers part of recovery. Add visible classifier failures/queue pressure and retention, then an explicit false-positive review workflow. Do not claim upstream provides a full appeals process. |
| Next | Same module: per-category thresholds | Useful after reviewing labelled results. Start with the requested unified 90%; tune categories from measured errors rather than copying upstream defaults. |
| Next | [Passive channel-monitor V2 and gentle backfill](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/docs/channel-monitor-v2-safe-defaults.md) | SAIAI already has Ops metrics and alerts. Evaluate passive success/latency reporting as an extension, avoiding extra model probes. Backfill must remain resource-bounded. Upstream documents V2 as explicit opt-in, not the default. |
| Next | [Backup record locking and retention](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/backend/internal/service/backup_retention.go) | SAIAI already has backup services. Audit concurrency, archive checkpoints and restore coordination before porting changes. Production restore remains a separate explicit operation. |
| Later, if needed | [Asynchronous image tasks](https://github.com/Wei-Shaw/sub2api/blob/b0a02a3304a0a78cdd003ddda1dd23ba02c03084/docs/ASYNC_IMAGE_TASKS.md) | Addresses long-running image requests and proxy timeouts; lower priority than current Claude/Codex input moderation. |

Do not bulk-merge gateway routing, OAuth rewriting, billing or client bootstrap:
SAIAI maintains specific tested contracts in those areas. Keep keyword blocking
and shared flagged-text caching out of this change; they need independent
false-positive and cache invalidation design. Upstream redacts retained excerpts;
SAIAI's current audit contract is stricter and does not retain raw prompt excerpts.
Jev resale/routing support in upstream README is distinct from using Jev internally
as a moderation detector; no new public System One endpoint is needed here.
