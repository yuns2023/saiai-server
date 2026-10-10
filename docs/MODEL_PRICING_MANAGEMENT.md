# Model pricing management

Administrators use `/admin/model-pricing` to inspect reference prices, billing
aliases, effective unit prices, service tiers and pricing provenance. The
existing settings link opens this page; existing aliases remain compatible.
The general settings form no longer resubmits price aliases, so a stale settings
tab cannot overwrite an edit made on the pricing page.

## Pricing contract

The reference feed remains community-maintained pricing, with the existing
local/bundled/hardcoded fallbacks. It is not an official provider feed or a
measurement of subscription account procurement cost. Official pricing pages
are linked for manual verification.

Each exact billed model may inherit another pricing model and override any of
its input, output, cache-read, 5-minute cache-write, 1-hour cache-write, or
Priority token prices. The editor accepts USD per million tokens; runtime
billing uses USD per token. Missing/null values inherit; explicit zero is free.
Overrides are keyed by the billed model identity before price aliases, never
by the borrowed price model or upstream account. An override for one alias
does not affect another alias of the same reference model. Routing and cache
TTL behavior are unchanged. No cache-category discount is introduced.

Reference resolution, model-specific policies, exact-model overrides, service
tier and long-context rules run through the shared BillingService. Priority
prices are independent from Standard overrides; Flex retains its existing
Standard tier multiplier. Equal/free cache-write prices still keep separate
5m/1h token accounting.

Effective unit prices enter TotalCost (the platform base charge). Existing
whole-request factors continue to determine ActualCost. Subscription quota
uses TotalCost; balance billing and API-key monetary limits retain ActualCost.
Subscription quota does not acquire a new group/model/user discount rule.
Per-image, video and native Chat per-turn billing remain separate. The preview
is a token-billing estimate and does not simulate account TTL overrides or
Gemini endpoint-specific legacy long-context settings.

## Administrator API

All routes require the existing administrator middleware and return no-store:

- `GET /api/v1/admin/settings/model-pricing`: search, configured filter and
  pagination over the price catalogue, built-in fallbacks and configured
  aliases/overrides, with recently billed models from the last 24h included.
  Configured and recent models sort first. Recent usage reads have a one-second
  deadline and a 30-second cache. Tier prices come from the billing calculator. `long_context_tiers` exposes
  whole-request long-context prices separately, only for supported billing tiers.
  The list can switch between short and long context; the editor shows both tables
  and the preview reports which rule applied. 272K means 272,000 input tokens,
  including cache, rather than kilobytes. Pricing UI follows existing Gateway tier
  behavior (including Priority exclusions); it is not an assertion of official
  pricing parity for every tier.
- `PUT /api/v1/admin/settings/model-pricing`: update one exact model's alias
  and unit prices, checking the displayed `expected_version` first. A 409
  requires reload/review; stale edits never overwrite newer runtime prices.
- `POST /api/v1/admin/settings/model-pricing/preview`: calculate a detached
  candidate configuration without saving, touching the live cache, or sending
  a provider request. Whole-request factors are explicit simulation inputs.
- `GET /api/v1/admin/settings/model-pricing/history`: the latest 100 edits,
  including actor, timestamp, old/new aliases and old/new prices. Every edit
  also emits an audit log.

Aliases, overrides and history persist atomically in three pricing settings
keys, preserving other settings. A successful save updates the serving
process's immutable configuration immediately; startup restores it from the
settings store. As with existing price aliases, additional Gateway processes
must reload/restart before they use edits made in a different process.
Automatic remote price refresh replaces reference data only and retains the
administrator overrides.

## Historical usage and migration

Migration `098_add_usage_pricing_snapshot.sql` adds nullable JSONB to usage
logs. Newly calculated text-token charges persist the exact reference/effective
prices, billed/resolved model identities, configuration/data version, service
tier and reference amount. HTTP, WebSocket and shared text-token billing recorders
use the same snapshot. The administrator usage tooltip shows the reference
amount, resolved model and version. Its fee column also displays an amber long-context
badge, with total input tokens, the strictly exceeded threshold, and separate input,
output, cache-read and cache-write multipliers. New snapshots retain the actual
calculator decision, including non-applied policies and the distinction between
whole-request and excess-input-only pricing. Early snapshots derive this decision
from their stored policy. Older NULL snapshots receive a clearly marked inference
only for known models; they cannot prove the historical alias/override configuration.
No current catalogue lookup or historical repricing is performed for usage display. Existing rows stay NULL; no historical
charge is recalculated or backfilled. Legacy split long-context snapshots
also retain the threshold and extra multiplier.

Apply the schema migration before running code that writes/reads the new
usage projection. This development change does not deploy or mutate production.

## Local validation

Targeted BillingService, Gateway/OpenAI usage, administrator-handler, DTO and
usage-repository tests pass, including explicit zero prices, inherited aliases,
cache splits, subscription charges, persisted snapshots, stale edits and
nonblocking billing reads during persistence. The related frontend suites cover the editor, usage badges, strict threshold
boundaries, recorded evidence, historical inference and tier exclusions. Frontend type checks/build and the embedded Server build also pass.
No provider/model traffic or production changes are part of these checks.
