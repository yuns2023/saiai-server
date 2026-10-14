# Pricing and quota review notes

Read this alongside [model pricing management](MODEL_PRICING_MANAGEMENT.md)
and [Codex usage limit resets](CODEX_USAGE_LIMIT_RESETS.md) when reviewing
partial price edits or administrative quota reconciliation.

## Partial pricing edits

A price override can change the calculator from a whole-tier multiplier to
explicit category prices. Review every category across that transition, even
when the administrator changes only one field.

- Compare input, output, cache read, 5-minute cache write and 1-hour cache write
  before and after the edit. Unedited categories must retain their intended
  inherited effective prices.
- Cover absent, explicit zero and positive overrides. Test Standard, Priority
  and Flex, and combine a Standard override with a partial Priority override.
- Verify both the actual charge and displayed unit prices. Round-trip the
  persisted pricing snapshot so historical display uses the same decision.
- Keep this in the shared BillingService so HTTP and both WebSocket recorders
  use the same calculation.

The regression fixture `TestModelPricingPartialPriorityOverridePreservesInheritedCachePrices`
checks that editing only Priority input does not reduce the inherited 1-hour
cache charge from 40 to 20 USD per million tokens. Those numbers are synthetic
fixture prices, not claims about any provider's current prices.

## Quota reconciliation

An atomic JSONB merge prevents unrelated fields from being lost, but it does
not prevent an older observation from replacing a newer value of the same
field. A successful reset response alone cannot prove that scheduling will
retain the reconciled state.

- Enumerate writers to both usage snapshots and account rate-limit flags.
  Include active writes, queued writes and asynchronous workers.
- Drain active writes before the authoritative usage fetch and reject queued
  observations preceding that boundary after reconciliation succeeds.
- Invalidate pre-reset throttle decisions so genuinely new observations can
  still update scheduling. Preserve unrelated account state.
- Use a blocking repository mock to control write ordering. Test queued old
  writes, fresh observations, unrelated accounts and cancellation separately.
- Keep the reset outcome and local synchronization result separate. Retry an
  uncertain operation with the same credit and idempotency key.

The current guard is shared through RateLimitService within one process.
Independent Gateway replicas and writers outside this guard are not covered.
Before extending that scope, design a database-level observation/version check
or another shared coordination mechanism and test cross-process ordering.

## Local validation and handoff

Run targeted checks serially with bounded resources:

```bash
cd backend
GOMAXPROCS=2 go test -p 1 -tags unit ./internal/service \
  -run 'Test(ModelPricing|CodexReset|OpenAIGatewayService_UpdateCodexUsageSnapshot|.*CodexSnapshot)' \
  -count=1 -timeout=180s
CGO_ENABLED=1 GOMAXPROCS=2 go test -race -p 1 -tags unit ./internal/service \
  -run 'Test(ModelPricing|CodexReset|OpenAIGatewayService_UpdateCodexUsageSnapshot|.*CodexSnapshot)' \
  -count=1 -timeout=180s
```

The race command requires an available C compiler. Report a missing compiler
as an unrun check; deterministic ordering tests do not replace the race
detector. Use CI for the full matrix. Vitest 4 does not accept `--minWorkers`;
use `--maxWorkers=1 --no-file-parallelism` for serial frontend checks.

For a read-only review, Go overlays can run temporary reproductions without
changing tracked files. A temporary path is not durable evidence: move the
relevant reproduction into a committed regression test when fixing the bug.

At closeout, inspect Git status, record the branch and commit IDs, distinguish
local commits from pushed or deployed changes, and preserve unrelated dirty
work. Production baselines and migration/release decisions come from current
private Ops records; do not infer them from a checkout's age or branch name.
