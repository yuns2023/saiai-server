# SAIAI native payment

SAIAI owns the payment order, provider configuration, callback verification,
and entitlement fulfillment lifecycle. The retired external payment iframe is not
part of this flow.

## Implemented scope

- Balance recharge and subscription-plan purchases.
- Administrators manage subscription products bound to active subscription
  groups. A subscription order snapshots its plan ID, group ID, normalized
  validity days, and server-side price before contacting the provider.
- EasyPay provider instances with Alipay and WeChat Pay methods.
- Provider credentials and per-order provider snapshots are encrypted with the
  server's existing secret encryptor.
- Payment is disabled by default. Enabling it requires at least one enabled
  provider instance.
- Payment callbacks use the order's encrypted provider snapshot, so rotating a
  provider configuration does not invalidate an existing order.
- Balance and subscription fulfillment reuse the existing transactional
  redeem-code service and are idempotent. Repeated callbacks cannot grant the
  same order twice; interrupted fulfillment can be resumed.
- Timed-out orders are reconciled with the provider before expiration. A
  PostgreSQL advisory lock limits the expiry worker to one server instance per
  pass. The same worker resumes paid or stale in-progress fulfillments after a
  process crash.
- Full-order refunds use a durable entitlement-first state machine. Balance
  credit or subscription time is reversed transactionally before the provider
  call. A definitive provider rejection restores that entitlement; an
  ambiguous transport result remains pending and is never retried blindly.
- Administrators can also record a fully manual refund performed in a provider
  console or offline channel. Manual completion and manual resolution of an
  uncertain automatic refund require a reason and external evidence reference.

## Architecture and provider extension

The payment domain owns orders, entitlement snapshots, state transitions,
auditing, expiry recovery, and idempotent fulfillment. Provider adapters own
only external protocol details. Balance orders grant metered-usage credit;
subscription orders grant or extend a subscription. Both use the same order
lifecycle without provider-specific branches.

SAIAI metered balance is USD-denominated credit, while a provider settles in
its configured ISO currency. Each provider instance therefore has a positive
`balance_credit_rate` (USD credit granted per one settlement-currency unit).
Balance orders persist that rate, keep entitlement `amount` separate from
provider `pay_amount`, and credit only the former. Subscription orders use the
plan's immutable price/currency and require a provider with the same settlement
currency. No implicit cross-currency conversion is allowed.

Provider adapters register a `provider.Definition` containing:

- a stable provider key and constructor;
- advertised payment methods and settlement currency;
- administrator-facing configuration metadata, including secret fields;
- untrusted notification order-ID extraction;
- provider-specific webhook acknowledgement bodies; and
- optional refund capability.

The registry validates adapter identity, advertised methods, currency, and
optional capabilities. Secret metadata drives API masking and error redaction.
The admin UI consumes `/api/v1/admin/payment/provider-definitions`, so adding a
new adapter does not require a new provider form or branches in payment
services. The generic callback endpoint is
`/api/v1/payment/webhook/{provider-key}`; callback signatures and merchant
identity are verified with the encrypted per-order provider snapshot.

## Endpoints

Authenticated user endpoints live under `/api/v1/payment`. The EasyPay callback
is available over GET and POST at:

`/api/v1/payment/webhook/{provider-key}`

`GET /api/v1/payment/plans` returns only in-sale plans whose target group is
still active and subscription-enabled. Administrative configuration,
providers, plans, orders, and fulfillment retries live under
`/api/v1/admin/payment`.

Refund administration is full-order only:

- `POST /api/v1/admin/payment/orders/{id}/refund` starts an `automatic` refund,
  or records a completed `manual` refund with an external reference.
- `POST /api/v1/admin/payment/orders/{id}/refund/resolve` records the manually
  verified outcome of an uncertain automatic refund. Confirming `not_refunded`
  restores the previously reversed entitlement in the same transaction.
- `force=true` is required when purchased balance has already been consumed or
  the remaining subscription duration is shorter than the purchased grant.
  This is an explicit reviewed exception and may create a negative balance or
  consume all remaining subscription time.

Every transition is idempotent and audited. The original encrypted provider
snapshot is used even after an instance is disabled or reconfigured. Recovery
replays only requests that were durably prepared but never sent. Once a provider
call may have started, recovery queries adapters implementing
`RefundQueryProvider`; otherwise the order remains `REFUND_PENDING` for manual
resolution. Partial refunds are intentionally unsupported because they would
make entitlement allocation and compensation ambiguous.
An automatically failed/refused attempt is never reissued through the generic
refund endpoint; an operator must verify the provider state and use the manual
path with external evidence if the refund is later completed out of band.

## Activation sequence

1. Apply migrations `085_native_payment_core.sql` and
   `086_native_payment_subscriptions.sql` in order.
   It disables the retired external purchase iframe flag while preserving the
   old URL value for reference.
2. Deploy the server and frontend while `payment_enabled=false`.
3. In **Admin → 原生支付**, create an EasyPay provider and any subscription
   plans. Plans may target only active subscription groups. Use the public SAIAI
   origin for both the callback and return URLs.
4. Keep the provider disabled while checking non-secret fields, then enable it.
5. Enable native payment globally.
6. Validate order creation and callback handling with the provider's sandbox or
   a local signed mock. Do not use a live user's provider token for diagnostics.
7. Since the old external payment system has no production orders, no historical
   order import or dual-callback period is required. Retire its service only
   after the native flow is enabled and observed healthy.

## Migration and release boundary

Migrations `085` and `086` are forward-only and transactional. They create the
native payment tables, seed the new feature as disabled, preserve the retired
purchase URL for forensic reference, and force the old purchase-entry flag to
false. They do not rewrite balances, subscriptions, users, usage records, API
keys, or provider accounts. An older application ignores the additive tables
and columns, so an application rollback does not require a database restore;
the native payment feature must remain disabled during that rollback.

Because the currently observed production baseline ends at migration `084`, a
release containing these migrations is database-affecting even when the SAIAI
local-proxy client remains unchanged. It must use the reviewed paired Gateway
maintenance path, not the no-migration Gateway fast path. Each independent site
needs its own migration/backup/rollback evidence. In particular, the existing
narrow abapi Gateway automation deliberately refuses this release class until
a separately reviewed migration-capable procedure exists.

Resource-bounded local release checks include:

```bash
cd backend
GOMAXPROCS=2 go test -mod=readonly -p 1 -tags integration \
  ./internal/repository \
  -run '^(TestMigrationsRunner_IsIdempotent_AndSchemaIsUpToDate|TestNativePaymentMigrations_UpgradeFrom084PreservesDataAndEnforcesContracts)$' \
  -count=1

GOMAXPROCS=2 go test -mod=readonly -p 1 ./internal/handler \
  -run '^TestNativePaymentEasyPaySignedCallbackAutomaticAndManualRefundE2E$' \
  -count=1
```

The migration test uses temporary PostgreSQL/Redis containers and validates
both a clean install and an `084` upgrade while preserving representative
existing data. The EasyPay test uses only `TEST_ONLY_*` credentials and a local
HTTP mock; it sends no real provider request.

## Rollback

Set `payment_enabled=false` first. Existing paid or in-progress orders and their
encrypted provider snapshots must remain available so callbacks and manual
fulfillment retries can finish. Application rollback does not require a database
restore, and provider instances with order history should be disabled rather
than deleted.
