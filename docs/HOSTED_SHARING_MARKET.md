# Hosted sharing market (P3a: owners sell their hosted accounts in price tiers)

An owner of a hosted Anthropic **setup-token carpool** account can list it
into a **price tier**: a group the admin marked with `market_tier`. Buyers
use API keys bound to the tier group and pay the tier's ordinary price.
For each sale the owner earns

```text
owner_earning = trunc(charged_amount × (1 − market_platform_cut_rate), 10)
platform_amount = charged_amount − owner_earning
```

where `charged_amount` is the buyer's actual balance change (balance before
minus balance after, read in the row lock of the deduction). The default cut
is 20 % (`market_platform_cut_rate` = 0.2). Earnings stay **frozen** for
`market_earning_freeze_days` (default 7), then become **available** and the
owner can convert them into their ordinary balance. Converted earnings are
ordinary balance in every respect: they pay for usage and can buy redeem
codes for other people ([USER_REDEEM_CODE_PURCHASE.md](USER_REDEEM_CODE_PURCHASE.md));
the site accepts that this works as an indirect payout. There is no
withdrawal.

The feature is generic and **off by default** (`market_enabled` = false).
The pilot site is ccyou.dev: the owner UI is on frontend-aog `/hosted`; the
classic console has the admin pages (tier switch, 共享设置, 共享收益,
settings section 共享市场). OpenAI accounts, saiai.top user pages and free
pricing (P3b) are not part of P3a.

Code: Server #230 (schema, listing lifecycle, migration 134), #232
(selection, reservations, final admission), #233 (atomic settlement, earnings
wallet, reconcile, migration 135), #234 (UI), #246 (removed the
share caps, allows zero user rate overrides on tiers, migration 136; see
Quota and Price under [Buyer and dispatch](#buyer-and-dispatch)). Design:
`merry-rolling-ladybug-p3-design.md` (sections R1/R2 override the body).
Builds on [HOSTED_SELF_ONBOARD.md](HOSTED_SELF_ONBOARD.md) (P2c owner groups)
and the P2b settlement events (migration 131).

## Rules that never change

- **Settlement is atomic with the charge.** A buyer-path sale writes its
  `shared_sale` settlement event and the owner's accrue entry in the same
  transaction as the balance deduction. If that cannot be written, the whole
  charge rolls back; there is no best-effort or shadow fallback.
- **Earnings follow the admission, not the current account row.** Every
  buyer-path request records an immutable `market_admissions` row (owner,
  owner version, listing version, buyer, key, request id) before the first
  upstream send. Settlement pays the owner named by that row, even if the
  owner was cleared or the account was deleted afterwards.
- **No buyer identity reaches the owner.** The owner ledger shows amount,
  status, account and model, never the buyer, key, request id or charged
  amount.
- **Self purchase earns nothing.** An owner buying from their own listing is
  charged normally; the event records `earning_zero_reason = self_purchase`.
- **The switch stops new sales only.** With `market_enabled` off no new
  buyer admission passes and owners cannot list; requests already admitted
  settle normally, and earnings stay visible and convertible.

## Roles and flows

### Admin

1. **Mark a tier.** Group form → 共享档位 (`market_tier` on
   `POST /api/v1/admin/groups` / `PUT /api/v1/admin/groups/:id`). A tier must
   be a standard (balance-charged) anthropic group, not hosted, with
   `rate_multiplier` > 0. It may not have its own `claude_code_only` or
   invalid-request fallback, and no other group's fallback may point into it
   (saving, promoting and every runtime hop are checked). Smart switch may
   enter a tier; the sale is then priced at the tier. A user rate override
   on a tier may be 0 (free self-use, for example the admin's own key): that
   user's price is then zero, so their requests never use a listed account
   and only the tier's platform accounts serve them (see Price under
   [Buyer and dispatch](#buyer-and-dispatch)).
   Negative overrides on a tier are refused (`MARKET_TIER_RATE_OVERRIDE`).
   Tiers may be exclusive or public, like any group; buyers bind keys to
   them as usual.
2. **Allow an account.** Account action → 共享设置
   (`PUT /api/v1/admin/accounts/:id/market-listing`). The account needs an
   owner (`MARKET_ACCOUNT_NO_OWNER`) and must be eligible: anthropic
   setup-token, carpool mode with a finite device limit and finite
   concurrency, no account-level `max_sessions`, and for a self-onboarded
   account a live, active proxy. The admin sets the allowed tiers (up to 32)
   and the protection parameters (`reserved_concurrency`, `reserved_devices`,
   `reserved_admissions`); omitted fields keep the stored value (site
   defaults on create), explicit 0 is kept. Reservations must leave buyers at
   least one slot and one device: `reserved_concurrency ≤ C − 1`,
   `reserved_devices ≤ device limit − 1`, `reserved_admissions ≤` admissions
   per cycle. An account that is already an admin member of a tier cannot be
   listed there (`MARKET_ADMIN_MEMBERSHIP_CONFLICT`), and admin binding paths
   never overwrite a market membership (`MARKET_MEMBERSHIP_CONFLICT`).
3. **Revoke.** 撤销共享 (`DELETE …/market-listing`): status `revoked`,
   nothing allowed, market membership removed, history kept. The owner
   cannot list again until the admin allows tiers again.
4. **Reverse frozen earnings.** `/admin/market-earnings` → 冲正冻结收益
   (`POST /api/v1/admin/market/earnings/reverse`), by owner and/or account,
   with the overview's `as_of` as cutoff. Only still-frozen, unreversed
   accrues are reversed; available earnings are never clawed back. See
   [Money semantics](#money-semantics).

Every listing change (owner or admin) runs in one transaction under the
account row lock, increments the listing `version`, appends a row to
`account_market_listing_events` and writes the scheduler outbox. These also
unlist (status `unlisted`, permission kept): clearing the owner, owner
pause, owner withdrawal request, and deleting or demoting the tier the
account sells into. Resume never relists; assigning an owner never lists.
Deleting the account revokes.

### Owner (ccyou.dev `/hosted` → 共享出售 / 共享收益)

- `GET /api/v1/hosted/owned-accounts/:id/market` shows the status
  (`not_configured`, `unlisted`, `listed`, `revoked`), the allowed tiers
  (name, rate, `available`, `admin_member`), the protection the admin set,
  `unavailable_reasons` and `can_list`. Only the current owner of a live
  account sees it; anyone else gets 404 `HOSTED_ACCOUNT_NOT_FOUND`.
- The owner picks **one** allowed tier and lists (`PUT … {"group_id":3,"listed":true}`),
  switches tier the same way, or stops (`{"listed":false}`). Listing needs
  the switch on; unlisting always works.
- Reason codes: `market_disabled`, `not_configured`, `no_allowed_tier`,
  `protection_invalid`, `platform_not_anthropic`, `not_setup_token`,
  `not_carpool`, `unlimited_devices`, `max_sessions_enabled`, `no_owner`,
  `owner_paused`, `withdraw_requested`, `proxy_unavailable`,
  `account_inactive`. The UI maps each to plain text, with a generic text for
  unknown codes.
- Earnings: summary (`frozen`, `available`, `converted_total`, `min_convert`,
  `as_of`), a keyset-paged ledger and 转入余额. Amounts are exact decimal
  strings (up to 10 decimals); the UI never converts them to floats. The cut
  rate and freeze days are not shown to owners.
- The block is hidden unless one of the owner's current accounts is listed
  or allowed into at least one tier, or the owner has earnings.

### Buyer and dispatch

Every candidate account is classified against the group actually scheduled
on (after `claude_code_only` / smart switch) and the authenticated key:

| Class | When | Capacity | Admission row / earnings |
|---|---|---|---|
| **buyer** | the group is a tier and the account is listed in that tier | buyer share (below) | yes / owner earns |
| **owner** | the account is listed, the group is an **owner group** of the user (below), the user is the current owner, the account is an admin member, and the billed key is the user's key of that group | total capacity, owner device class | no / none (owner pays the hosted fee) |
| **platform** | the group is a tier and the account is an admin member with no owner and not listed | the account's own capacity | no / platform keeps all |
| **excluded** | any other combination involving a listed account: other groups, ungrouped, Simple mode, unresolvable group, pinned-mode accounts in a tier, any path without a final admission (count_tokens, Ops retries, other handlers) | never selected | — |

An account that is neither on the market nor in a tier takes exactly the old
code path. An owner buying through a tier key is a buyer (`self_purchase`),
not the owner path.

**Owner group.** Checked in the database on the admission snapshot: the
group is live, active, Anthropic, standard, hosted usage on, exclusive, not
a tier, with a finite fee rate ≥ 0; the owner has a `user_allowed_groups`
row and no other user has one; `hosted_owner_groups` does not map it to
another user; every live member account (any source) is owned by the owner
(an ownerless platform account fails). P2c groups (mapped by self-onboard
approval) qualify, and so does an exclusive hosted group an admin builds for
an admin-assigned (P1) owner: allow only the owner, put only that owner's
accounts in it, and the owner's key of that group uses the listed account
through the owner path. An owner may have several such groups, each with its own fee rate. The check
runs before the owner device registration (a refused candidate registers
no device and uses no cycle admission) and again before the first send; a
refusal or DB error fails over and is never downgraded to another path.

**Final admission.** After the device check and pricing, before the first
upstream send, a buyer attempt runs one REPEATABLE READ transaction that
share-locks the listing row and re-checks everything on one snapshot: switch
on, market membership, listing still listed in this tier with the same
version, same owner and owner version, owner user active, account eligible
and schedulable (status, expiry, rate limit, overload, pause/withdraw,
proxy), same concurrency, reservations and device policy, the trusted-user
and key allowlists, the billed key group = the tier, and a positive
effective rate (the buyer's user rate override on the tier if there is one,
else the tier rate) and model rate factor. It then inserts the
`market_admissions` row. Owner and platform attempts run the same reads
without writing a row. Any refusal fails over before upstream work; a DB
error or unknown commit is confirmed by attempt id or the candidate is
dropped. A cancelled request never sends after its admission.

**Dual concurrency.** `concurrency:account:{id}` stays the total set; buyer
leases are also members of `concurrency:account_buyer:{id}`. A buyer gets a
slot only if `total < C` and `buyer < C − R` (`R` = `reserved_concurrency`;
a buyer cap ≤ 0 is refused, never "unlimited"). Owner and platform attempts
check only the total. Example: C = 4, R = 1 → three buyers plus the owner.
The reservation is a slot guarantee; there is no FIFO or wait-time promise.
Buyer leases are renewed in both sets and must still be held right before
the first send.

**Device split.** Inside the existing device-registry WATCH transaction:
total devices `< limit` and buyer devices `< limit − reserved_devices`.
Device records carry a class (owner / buyer; records from before the market
count as buyer). A buyer request on an owner device does not inherit the
exemption. Cycle admissions: total `Q`, buyers `Q − reserved_admissions`;
`Q = 0` means unlimited, `Q − reserved_admissions ≤ 0` means buyers cannot
add a device this cycle. An unidentified device or a registry error makes the
buyer candidate fail over; on the owner path a registry error fails open (see
limitations).

**Quota (no share caps).** No usage quota is reserved for the owner: a
listed, otherwise eligible account sells as soon as it is listed, and its
5h/7d quota is shared by the owner and the buyers. When the quota is used
up, upstream answers 429 and the ordinary rate-limit handling takes the
account out of scheduling until the reset, for buyers and owner alike, like
any platform account. The owner keeps only the slot and device reservations
above. (P3a first shipped 5h/7d share caps and a usage-sample freshness gate;
they were removed. The columns `cap_5h_pct`, `cap_7d_pct` and
`sample_max_age_minutes` of `account_market_listings` and
`account_market_listing_events` stay in the schema, unused, so the previous
binary still works after a rollback; the server no longer reads or sets
them: new listings take the column defaults, history rows copy the row.
The old Redis keys `market:usage_obs:{account}` are no longer written and
expire on their own, at most 8 days after their last write.)

**Price.** Before dispatch the buyer candidate needs a positive price: model
as the setup-token account sends it, positive input/output price, effective
group rate (user override included), model factor and user discount of the
billed key ([BILLING_FACTORS.md](BILLING_FACTORS.md)). A zero or missing price
excludes buyer candidates; platform candidates remain. The final admission
re-reads the buyer's user rate override and the tier's rates in its own
snapshot, so an override set to 0 while the request waited (or hidden by the
gateway's user-rate cache) refuses the sale before any upstream send. This
is why a zero override on a tier is safe: a request whose override is 0 in
the admission snapshot is never sent to a listed account, writes no
admission and no `shared_sale` event, and the owner earns nothing from it;
the tier's platform accounts serve it. Admins do not need to unlist anything
before setting a zero override (optional). See the limitation on the cache
window below.

**count_tokens in a tier** uses platform accounts only (anthropic, not
Bedrock, not pinned), each re-checked in the database, with no Messages
sticky read or write. No such account configured → `404 not_found_error`;
configured but none available now → `503 api_error` ("Service temporarily
unavailable"). A listed account never serves count_tokens, not even for its
owner. Measured with Claude Code 2.1.326: on 404 or 503 `/context` falls back
to a local estimate with no error shown; 404 is not retried, 503 is retried
once per call.

## Settings (系统设置 → 共享市场)

`GET/PUT /api/v1/admin/settings` (flat keys). A PUT writes only the market
keys present in the request, so a stale tab cannot write old market values
back. An out-of-range value returns 400; a missing or invalid stored value
reads as its default; an explicit 0 is kept wherever 0 is in range.

| Setting | Default | Allowed | Meaning |
|---|---|---|---|
| `market_enabled` | `false` | boolean | Master switch: listing and new buyer admissions |
| `market_platform_cut_rate` | 0.2 | 0–1, ≤ 4 decimals | Platform share of `charged_amount`; read inside the billing transaction |
| `market_earning_freeze_days` | 7 | 0–90 | Frozen period; snapshotted per sale as `earning_available_at` (changing it does not move existing entries) |
| `market_min_convert_usd` | 1 | 0.01–10000, ≤ 2 decimals | Smallest conversion; checked at conversion time |
| `market_default_reserved_concurrency` | 1 | 0–100 | Default `reserved_concurrency` (R) for new listings |
| `market_default_reserved_devices` | 2 | 0–32 | Default `reserved_devices` |
| `market_default_reserved_admissions` | 1 | 0–32 | Default `reserved_admissions` per cycle |

The `market_default_*` values only seed new listings; per-listing values have
the same static bounds plus the per-account bounds above. Stored values of
the removed keys `market_default_cap_5h_pct`, `market_default_cap_7d_pct`
and `market_default_sample_max_age_minutes` are ignored (a request that
still sends them is not an error; they are simply not read).

## API

All under the standard `{code, message, data}` envelope. Owner endpoints
require a signed-in user and act only on that user; admin endpoints require
an admin.

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/api/v1/hosted/owned-accounts/:id/market` | owner | Listing view, tiers, protection, reasons |
| PUT | `/api/v1/hosted/owned-accounts/:id/market` | owner | `{group_id, listed:true}` list / switch tier; `{listed:false}` stop |
| GET | `/api/v1/hosted/earnings` | owner | Frozen / available / converted, `min_convert`, `as_of` |
| GET | `/api/v1/hosted/earnings/entries?cursor=&limit=` | owner | Own ledger, keyset by entry id, limit 1–100 (default 20) |
| POST | `/api/v1/hosted/earnings/convert` | owner | `{amount, request_id (UUID)}` → balance |
| POST / PUT | `/api/v1/admin/groups`, `/api/v1/admin/groups/:id` | admin | `market_tier` on group create / update |
| GET | `/api/v1/admin/accounts/:id/market-listing` | admin | Listing, allowed tiers, `ineligible_reason`, defaults |
| PUT | `/api/v1/admin/accounts/:id/market-listing` | admin | `allowed_group_ids` (required, may be `[]`) + protection fields |
| DELETE | `/api/v1/admin/accounts/:id/market-listing` | admin | Revoke (404 `MARKET_LISTING_NOT_FOUND` if never configured) |
| GET | `/api/v1/admin/market/earnings?owner_user_id=&page=&page_size=` | admin | Per-owner summary + `as_of` |
| GET | `/api/v1/admin/market/earnings/entries?owner_user_id=&cursor=&limit=` | admin | Ledger with settlement / reverse / convert refs and notes |
| POST | `/api/v1/admin/market/earnings/reverse` | admin | `{account_id?, owner_user_id?, as_of, note?}` (≥ 1 scope, note ≤ 500) |
| GET | `/api/v1/admin/market/admissions/:id` | admin | Sale audit by admission id |
| GET | `/api/v1/admin/settlement/reconcile` | admin | Now includes a `market` section |
| GET / PUT | `/api/v1/admin/settings` | admin | `market_*` settings |

The **sale audit** returns the admission row, the admission-time price
snapshot (`admission_price`: model, price version, group rate, model rate,
user discount, unit prices; null if the admission was never billed) and the
settlement (`charged_amount`, `owner_earning`, `platform_amount`,
`platform_cut_rate`, `earning_zero_reason`, `earning_available_at`,
`split_conserved`, and from the usage log `model`, `billing_model`,
`final_price`). The usage-log fields are null when the log is missing or
purged by retention; the event columns are always present.

## Error codes (`reason` in the error body)

| Code | HTTP | Meaning |
|---|---|---|
| `MARKET_DISABLED` | 409 | Listing while `market_enabled` is off |
| `MARKET_ACCOUNT_NO_OWNER` | 409 | Admin allows tiers for an account without an owner |
| `MARKET_ACCOUNT_NOT_ELIGIBLE` | 409 | Account cannot be shared; `metadata.reason` = reason code |
| `MARKET_GROUP_NOT_ALLOWED` | 400 | Owner picked a tier the admin did not allow |
| `MARKET_INVALID_TIER` | 400 | Allowed ids are not live tiers of the account's platform, or more than 32 |
| `MARKET_INVALID_PARAMS` | 400 | Protection out of range or over the per-account bounds |
| `MARKET_ADMIN_MEMBERSHIP_CONFLICT` | 409 | Account is already an admin member of that tier |
| `MARKET_MEMBERSHIP_CONFLICT` | 409 | Admin binding hits a pair the owner listed; revoke first |
| `MARKET_LISTING_REVOKED` | 409 | Owner action on a revoked listing |
| `MARKET_LISTING_NOT_FOUND` | 404 | Admin revoke of a never-configured listing |
| `HOSTED_ACCOUNT_NOT_FOUND` | 404 | Owner endpoint on an account the user does not currently own |
| `INVALID_MARKET_TIER` | 400 | Group cannot be a tier (not standard / hosted / rate ≤ 0 / not anthropic) |
| `MARKET_TIER_FALLBACK` | 400 | A fallback chain would enter or leave a tier |
| `MARKET_TIER_RATE_OVERRIDE` | 400 | Negative user rate override on a tier (0 is allowed) |
| `MARKET_TIER_GUARD_UNAVAILABLE` | 503 | Tier checks could not run |
| `MARKET_EARNING_INVALID_AMOUNT` | 400 | Convert amount not positive or > 2 decimals |
| `MARKET_EARNING_INVALID_REQUEST_ID` | 400 | `request_id` is not a non-nil UUID |
| `MARKET_EARNING_BELOW_MINIMUM` | 400 | Below `market_min_convert_usd` (`metadata.min_convert`) |
| `MARKET_EARNING_INSUFFICIENT` | 409 | More than available (`metadata.available`) |
| `MARKET_EARNING_CONVERT_CONFLICT` | 409 | `request_id` reused with a different amount |
| `BALANCE_OUT_OF_RANGE` | 400 | Resulting balance would overflow |
| `MARKET_EARNING_INVALID_QUERY` | 400 | Bad cursor, limit, owner or admission id |
| `MARKET_EARNING_REVERSE_SCOPE_REQUIRED` | 400 | Reverse without `account_id` or `owner_user_id` |
| `MARKET_EARNING_REVERSE_AS_OF_INVALID` | 400 | `as_of` missing, not RFC3339, or later than server time |
| `MARKET_EARNING_NOTE_TOO_LONG` | 400 | Reverse note over 500 characters |
| `MARKET_EARNING_UNAVAILABLE` | 503 | Earnings service not available |
| `MARKET_EARNING_RECORD_IMMUTABLE` | 400 | Admin tried to delete or expire a `market_earning` balance record |
| `MARKET_ADMISSION_NOT_FOUND` | 404 | Sale audit for an unknown admission id |

## Money semantics

**Atomic `shared_sale` settlement.** The billing command of the served
attempt carries `MarketPath` plus a proof (admission id, attempt id,
request ref, account, group, buyer, key), frozen before the async enqueue.
The usage billing repository picks the market branch before anything else
(also before the shadow event's 3 s low-budget skip) and runs one
transaction: dedup claim → load the admission and compare every proof field
→ balance deduction → `shared_sale` event → guarded accrue. A missing proof,
a field mismatch, an unknown admission or a subscription charge is an
integrity error: the whole charge rolls back with an `ALERT` log and
`market_integrity_errors` is counted. Replays use the unchanged
`(request_id, api_key_id)` dedup: the same command is a no-op; another
admission under the same identity is a conflict. The admission is bound to
its request and key, so it cannot settle twice. The billing transaction
never locks the seller's wallet or user row, so mutual buying cannot
deadlock. Zero-earning reasons: `self_purchase`, `zero_charge`,
`owner_mismatch` (owner version no longer names that owner), `other` (cut
= 1 or an earning below 1e-10); the event is committed without an accrue.
Overdraft: earnings use the full charged amount, so the platform bears the
bad debt of concurrent requests the same balance admitted.

**Pricing promise (accepted pilot limitation).** The admission-time price
snapshot decides only whether the account is sellable and is stored for
audit in `usage_settlement_events.admission_price`. It is not a buyer quote.
The charge follows ordinary pricing at billing time, so a price change
between admission and billing (long streams, worker queue) applies; a change
to 0 settles as `zero_charge`. The split is always conserved on
`charged_amount`.

**Reconcile rule.** `GET /api/v1/admin/settlement/reconcile` → `market`:
summary (sales, charged, earning, platform; accrued, reversed, converted)
and issues `accrue_missing`, `accrue_mismatch`, `accrue_unexpected`,
`split_mismatch`, `reverse_mismatch`, `convert_history_missing`,
`convert_history_orphan`, `admission_without_event`. A snapshot price
different from the final price is **normal**; only
`charged_amount ≠ owner_earning + platform_amount` is a split anomaly. An
admission without an event is `pending` for 10 minutes, then
`undetermined`; it is never paid out automatically (failed candidate
attempts land here too). Only owner-backed sales are covered; platform
sales in a tier write no event.

**Convert.** Idempotent by `(owner, request_id)`: the same id and amount
return the original result (`replayed: true`) even if the money was spent
or the minimum changed since; a different amount is a 409. Then the minimum,
≤ 2 decimals and ≤ available are checked, and one transaction writes the
convert entry, adds the amount to `users.balance` and writes a `redeem_codes`
history row (`type = market_earning`, `status = used`,
`code = MKTEARN-<entry id>`). That row appears in the user's balance history
but is not counted as a real top-up (balance history totals, invite
eligibility). The balance trigger may raise the user's auto level like any
top-up. Available = accrues and reverses with `available_at ≤ t` plus
converts, with `t` taken after the wallet lock.
The frontend stores the pending conversion `{request_id, amount}` in
localStorage per user before sending and replays it unchanged on timeout,
network error or 5xx until a definite answer, also after the dialog closes
or the page is reloaded. Its local read/check/write steps run under a
per-user Web Lock (`ccyou-market-convert-<userId>`), never across the HTTP
call, so two tabs cannot start two conversions. An unreadable record blocks
new conversions until the user clears it.

**Reverse.** `as_of` is a required cutoff, not an idempotency key. A reverse
acts on the scope's earnings that are committed when it runs, recorded at or
before `as_of`, and still frozen and unreversed at its lock time. Every call,
retries included, selects its scope again; a retry does not replay the first
attempt's entry set. Take `as_of` from `GET /api/v1/admin/market/earnings`,
not from the browser clock. By owner affects only that owner; by account can
affect several past owners of the account; give both to touch one owner's
earnings on one account. Each accrue can be reversed once.

## Deployment

**Migrations.**
- `134_market_listings.sql` (one transaction): `groups.market_tier` + CHECK,
  `account_groups.source`, `account_market_listings` + events,
  `market_admissions`, the `shared_sale` columns and four CHECKs on
  `usage_settlement_events` (one ALTER, one scan), `owner_earning_wallets`,
  `owner_earning_entries`, insert-only and tier-guard triggers.
- `135_market_admission_price.sql`: `usage_settlement_events.admission_price`
  JSONB, catalog-only (no default, no CHECK).
- `136_market_tier_allow_zero_override.sql`: `CREATE OR REPLACE FUNCTION`
  of the two tier-guard trigger functions from 134 so that only negative or
  NaN user rate overrides are refused on a tier (0 is allowed). Function
  bodies only: no table, trigger, lock on user tables or scan; safe on the
  large sites.
- A site still on 131 (ccyou.dev at the time of writing) also applies
  `132_redeem_code_purchases.sql` and `133_hosted_self_onboard.sql` in the
  same upgrade; 133 aborts on duplicate upstream UUIDs, run its pre-check
  from [HOSTED_SELF_ONBOARD.md](HOSTED_SELF_ONBOARD.md) first. A site older
  than 131 creates `usage_settlement_events` in the same run (empty).

Every file sets `lock_timeout = '5s'` and `statement_timeout = '60s'` and
runs in its own transaction. If a lock is not granted within 5 s, that file
rolls back completely, earlier files stay applied, and the server fails to
start; clear the blocker and start again.

**Pre-deploy checks (read-only).**

```sql
SELECT to_regclass('public.usage_settlement_events') IS NOT NULL AS has_events;
SELECT 'usage_settlement_events' AS t, count(*) FROM usage_settlement_events
UNION ALL SELECT 'groups', count(*) FROM groups
UNION ALL SELECT 'account_groups', count(*) FROM account_groups;

-- long transactions and lock waits
SELECT pid, usename, state, now() - xact_start AS xact_age,
       pg_blocking_pids(pid) AS blocked_by, left(query, 80) AS query
  FROM pg_stat_activity
 WHERE datname = current_database()
   AND xact_start IS NOT NULL
   AND (now() - xact_start > interval '30 seconds'
        OR cardinality(pg_blocking_pids(pid)) > 0)
 ORDER BY xact_start;
```

Skip the first count if `has_events` is false. Groups and account groups are
expected in the tens to hundreds; a large `usage_settlement_events` makes
the CHECK scan longer than usual. Do not deploy while a long transaction
touches these tables.

**Single-container drain deploy.** At startup a process removes every slot
member of other processes from the concurrency sets (total and buyer) and
deletes the wait counters. If an old and a new process overlap, the new one
wipes the old one's live leases and the reservations no longer hold. Deploy
with the switch off: drain and stop the old process, start the new one
(migrations run at its startup), and only then create tiers and listings and
turn the switch on.

**Rollback order.**
1. Set `market_enabled` = false (new buyer admissions stop; the registry
   polls every 2 s). Let in-flight requests finish and settle.
2. Set `market_tier` = false on every tier group. Demotion removes the
   market memberships and unlists the listings; an old binary would treat
   leftover market rows as ordinary memberships.
3. Drain and deploy the old binary. The additive schema, the ledger and the
   admission history stay.

The binary before migration 136 runs against the 136 schema unchanged: it
still reads and writes the kept share-cap columns (with the values stored
there, so its share caps and sample gate apply again: listed accounts sell
only after a fresh observation) and its own save-time check refuses new
zero overrides on a tier. Zero overrides stored on tiers meanwhile need
attention before rolling back: the old binary refuses every save of a tier
group that has one (`MARKET_TIER_RATE_OVERRIDE`), and its final admission
does not re-read the buyer's override, so a stale cached positive rate
could admit such a user to a listed account. Before deploying the old
binary, delete or raise the zero overrides on tiers (or demote the tiers,
step 2, or keep `market_enabled` off). Right after the rollback the old
binary may briefly refuse all sales: the scheduler snapshots in Redis
(`sched:acc:*`) written by the new binary carry listings without
`sample_max_age_minutes`, which the old binary reads as 0 (sample gate
refuses) until it rebuilds the snapshots from the database.

**Pre-launch acceptance gates (on the target site, switch on, test
accounts).**
- Stale snapshot write-back: write back an old cached copy of a listed
  account without market fields (as a whole-row `UpdateLastUsed` does) and
  confirm it is never dispatched through an ungrouped or plain group request.
- Ops client retry: with an account bound to a plain group by the admin and
  listed in a tier, an Ops client-mode retry must skip it, and a pinned retry
  must refuse it.
- Also from the plan: C = 4, R = 1 with the owner busy (owner 1 + buyers 3);
  a user with a 0 override on the tier is served by a platform account
  only; settlement to the original owner after the owner is cleared; convert/reverse across the maturity instant; count_tokens
  200/404/503 with the pilot's Claude Code version.

## Known limitations (accepted)

Listing and tiers
- Deleting or demoting a tier unlists the listings that sell into it and
  removes it from every allowed set; the rest of the permission stays (it is
  not a revoke).
- The admin group form sends `market_tier` only when the toggle was changed,
  but the group PUT itself has no version check: an API client that sends a
  stale `market_tier = false` demotes the tier.
- Admin share-settings modal: the UI re-reads the listing right before
  saving and reloads if the owner, version or status changed, but the
  server PUT has no version check, so a change between that read and the
  save is not detected.
- When an admin save returns `MARKET_TIER_RATE_OVERRIDE` or
  `MARKET_MEMBERSHIP_CONFLICT`, fields saved before that step may already be
  committed. Those pre-existing save paths show the server's English
  message.
- Tier groups with `show_account_quota` also show listed accounts in the
  hosted quota view.

Dispatch
- Cold-start registry window (user decision): if the listing registry never
  loads at startup (database unavailable for about 20 s), a listed account
  whose cached copy predates both the listing and the owner assignment could
  be dispatched as an ordinary account until the first successful poll.
  Until then only accounts with an owner are treated as listed.
- No usage quota is reserved for the owner (user decision): buyers can use
  up the account's 5h/7d quota, after which the owner waits for the reset
  like the buyers.
- Owner count_tokens is never served by a listed account; if the owner's
  hosted group has no other account, the owner's count_tokens returns 503
  and Claude Code uses its local estimate.
- Zero-override cache window: the gateway caches user rate overrides
  (default TTL 30 s, configurable; a lookup failure falls back to the group
  rate). For that long after an override is changed to 0, candidate
  selection may still use the old positive rate: a listed account can be
  picked, take a buyer lease, register a buyer device and use a cycle
  admission before the final admission re-reads the database value and
  refuses (no sale, no upstream send, no owner earning; the request fails
  over to a platform account). These side effects happen per candidate
  attempt and can accumulate across concurrent requests and candidates, but
  stay within the buyer-side limits (the owner's reservations hold); the
  refusal does not give back registered devices or used cycle admissions.
  In the same window a platform account may still bill that user at the old
  positive rate (the general override cache, as on any group).
- On the owner path a device-registry error fails open (logged, dispatched
  without a recorded device), like plain carpool.
- A refusal after the device admission leaves that device and its cycle
  admission recorded on the refused account; the initial-serial permit is
  consumed even when the attempt sends nothing.
- Simple run mode never selects listed accounts and never bills a market
  command; the market is not offered there.

Money
- Price lock downgraded: the charge follows the price at billing time, not
  the admission snapshot (see [Money semantics](#money-semantics)).
- A reverse retry re-scopes; it is not idempotent.
- Overdraft bad debt of concurrent requests is borne by the platform.
- No Web Locks on old browsers: the conversion steps then run unlocked, and
  two tabs racing at the same moment could both start a conversion (each
  with its own request id, still limited by the available amount).
- The reverse note is required by the UI only; the API accepts none.
- Reconcile window queries on `owner_earning_entries.created_at` and
  `market_admissions.admitted_at` have no time-leading index (follow-up
  before the market grows). The orphan check reads convert codes with up to
  18-digit entry ids.

Not implemented
- P3b free pricing (owner minimum rate, buyer maximum rate, session price
  lock) is not implemented.
