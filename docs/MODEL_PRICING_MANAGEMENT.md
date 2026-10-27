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
Standard tier multiplier.

OpenAI publishes no Priority/Fast tier for some models and serves such
requests at Standard. For the feed entries listed in
`pricing_reference_corrections.go` (see the pricing README), Priority bills
exactly like Standard, including the Standard long-context rule, instead of
the generic x2 multiplier; the reference amount does the same. Flex is
unchanged. An administrator Priority override for the billed model is still
honoured as intentional operator pricing: overridden categories use the
override and every unedited Priority category inherits the Standard reference
price (not x2), with the usual Priority long-context exclusion. The pricing
list and preview show these effective prices, `effective`/`reference` carry
`no_priority_tier`, and usage snapshots record
`priority_billed_as_standard` when Priority was billed at Standard. Aliases
follow the resolved feed entry: an alias priced from a listed model bills
Priority at Standard; a listed model aliased to another entry uses that
entry's Priority prices. Equal/free cache-write prices still keep separate
5m/1h token accounting. A partial Priority override preserves the inherited
1-hour cache-write price, including when Priority originally used a whole-tier
multiplier; the effective 1-hour Priority price is retained in usage snapshots.
Once a Priority field is overridden on a multiplier model, every unedited
Priority category is the reference price times two (times one for models
without an official Priority/Fast tier), including an inherited zero. A Standard cache-write override never sets the Priority 1-hour price:
it stays what Priority charges without that override (the native or overridden
Priority cache-write price for models without a separate 1-hour price;
otherwise the reference 1-hour price, doubled for multiplier models). Known behaviour, deliberately unchanged: for native
Priority models, a category without a published Priority price still uses the
Standard price, including a Standard override. Usage snapshots written before
`priority_synthesized` existed keep their original display and recalculation.

Effective unit prices enter TotalCost (the platform base charge). Whole-request
factors determine ActualCost: the effective group/user-specific group rate,
the billed-model rate, and the effective user discount. Balance billing,
subscription window usage and API-key monetary limits all use ActualCost.
Subscription and balance pricing share the same configuration; only the source
of the charge differs. An explicit zero group, model or user factor makes the
user charge zero, including subscription quota consumption, without falling
back to wallet billing. TotalCost still records the base charge, and account
quota continues to use TotalCost times the account rate. The system-default
configuration helper retains its existing fallback to 1 for a non-positive
default. Existing usage and balances are not recalculated by this change.
Configured factors begin affecting new subscription requests when this version
is deployed; existing non-1 group/model rates and user discounts also apply.
No schema migration or historical quota backfill is required.
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
  a provider request. The Web editor exposes only a model-rate simulation input, with group and
  user factors fixed at 1 in either billing mode. It is excluded from
  price-setting saves. The API still accepts explicit group and user factors
  for a full billing simulation.
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
usage projection. The application startup migration runner applies 098 before
serving requests; an image-only successor at the retained 098 schema does not
need another migration. Deployment authorization and per-site migration/rollback
evidence remain separate operational requirements.

## Maintenance and review boundaries

The contract is implemented in `backend/internal/service/model_pricing.go`,
`model_pricing_settings.go` and `billing_service.go`. Administrator API DTOs
and usage recorders must retain the same units and snapshot. Frontend
`src/utils/usageLongContext.ts` resolves recorded decisions before stored
policies and, last, explicitly labelled known-model historical inference.
An explicit recorded `applied=false` wins over every inference. Count total
input including cache once, and apply the strict `>` threshold.

Priority currently bypasses the Gateway whole-request long-context multiplier,
in both calculation and snapshot creation, except where Priority is billed as
Standard for a model without an official Priority/Fast tier (no administrator
Priority override). The pricing UI reflects that exclusion. Any proposal to match newer official Priority long-context prices
requires a separate billing review, fixtures for every transport and a decision
about future records; this display change does not resolve that policy question.
The reference feed is community pricing even when an official pricing link is
shown. Subscription procurement cost, reference amount, platform base charge
and the discounted user charge must remain distinguishable.

## Local validation

See [pricing and quota review notes](PRICING_AND_QUOTA_REVIEW.md) for partial
override regression checks and the related asynchronous-write review method.

Targeted BillingService, Gateway/OpenAI usage, administrator-handler, DTO and
usage-repository tests pass, including explicit zero prices, inherited aliases,
cache splits, subscription charges, persisted snapshots, stale edits and
nonblocking billing reads during persistence. The related frontend suites cover the editor, usage badges, strict threshold
boundaries, recorded evidence, historical inference and tier exclusions. Frontend type checks/build and the embedded Server build also pass.
No provider/model traffic or production changes are part of these checks.

### Usage context size versus pricing

The administrator Token column independently marks input context above the
model's own long-context threshold, for example `Input context >272K`. It adds
uncached input, cache reads and cache writes once, excludes output, and applies
regardless of service tier or whether a surcharge was applied. Only models with
a long-context price tier get the marker: the threshold comes from the stored
pricing snapshot (calculator evidence, then the stored policy), and rows without
a snapshot fall back to the known GPT long-context models at 272K. Models
without a tier, such as Claude, never show it. Images and native Chat are
excluded.
The fee badge is explicitly labelled `Long-context surcharge` and still follows
stored billing evidence (or the existing labelled historical inference).
Priority/Fast has a visible text label beside its service-tier icon; the actual
billed policy remains in the fee details and snapshot. Historical charges and
billing logic are unchanged.
