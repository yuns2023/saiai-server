# User-bought balance redeem codes

A signed-in user can spend balance on a balance redeem code and give the code
to someone else, as a gift or to pay for another user. Any balance can be
spent: paid top-ups, the new-user `default_balance`, promo-code bonuses and
admin grants alike. There is no "paid balance only" cap; that is an operator
decision.

The bought code is an ordinary unused `balance` redeem code with no expiry.
Anyone, the buyer included, redeems it through the unchanged redeem flow
(`POST /api/v1/redeem`), which adds its value to the redeemer's balance.

The feature is generic for every site and is off by default. ccyou.dev
(frontend-aog) is the only user UI: on `/billing` (充值), the sections
购买兑换码 and 我买的兑换码. The classic console has the admin settings and
labels for the new history records, but no user purchase page.

## Admin settings

Classic admin console: 系统设置 → 用户默认值 → 用户购买兑换码. Admin API:
`GET` / `PUT /api/v1/admin/settings`. The settings are not public; users see
the values they need in `GET /api/v1/redeem/purchases`.

| Setting | Default | Allowed values |
|---|---|---|
| `redeem_purchase_enabled` | `false` | boolean |
| `redeem_purchase_min_amount` | 1 | 0–1,000,000 USD, at most 2 decimals |
| `redeem_purchase_max_amount` | 500 | 0–1,000,000 USD, at most 2 decimals |
| `redeem_purchase_window_max_amount` | 1000 | 0–1,000,000 USD, at most 2 decimals; per buyer, rolling 24 h |
| `redeem_purchase_window_max_count` | 20 | 0–10,000; per buyer, rolling 24 h |

- The PUT also requires min ≤ max. A field absent from the PUT (or `null`)
  keeps its stored value. Changes are listed in the settings audit log line.
- **0 means "none allowed", not unlimited.** A max amount, window amount or
  window count of 0 blocks every purchase. A min amount of 0 only removes the
  lower bound; the amount must still be above 0.
- Both 24 h caps count all of the buyer's purchases created in the last 24 h,
  **revoked ones included**. A revoke returns the money, not the window room.
- A missing or invalid stored value (only possible through a direct database
  write) falls back to that setting's default; the switch falls back to off.
- A purchase reads the settings inside its own transaction, after locking the
  buyer row, with no cache. A switch-off or a lowered cap therefore applies
  to the next purchase, including one already waiting on the lock. If the
  settings cannot be read, the purchase fails.

## Switching off

Turning `redeem_purchase_enabled` off only blocks new purchases (403
`REDEEM_PURCHASE_DISABLED`). Bought codes can still be redeemed, the list
still works, unused codes can still be revoked, and a retried request that
already succeeded still returns its original purchase. On ccyou.dev a user
with earlier purchases sees 本站暂停了购买兑换码 instead of the form, above the
list; for other users the section is hidden.

## User API

All three endpoints require a signed-in user (JWT) and act on that user only.
Responses use the standard envelope: `{"code": 0, "message": "success",
"data": …}` on success, and `{"code": <HTTP status>, "message": …,
"reason": "<CODE>"}` on errors. A request body that is not valid JSON for the
request fields gets a plain 400 without `reason`.

### `GET /api/v1/redeem/purchases?page=&page_size=`

Lists the user's purchases, newest first, together with the form state.
`page_size` (or `limit`) defaults to 20; values from 51 to 1000 are cut to
50, and invalid or larger values fall back to 20. It works with the switch
off.

```json
{
  "config": {
    "enabled": true,
    "min_amount": 1,
    "max_amount": 500,
    "remaining_amount_24h": 990,
    "remaining_count_24h": 19
  },
  "items": [
    {
      "id": 7,
      "code": "<32 hex characters>",
      "amount": 10,
      "status": "unused",
      "used_at": null,
      "created_at": "2026-11-17T08:00:00Z",
      "revoked_at": null
    }
  ],
  "pagination": {"total": 1, "page": 1, "page_size": 20, "pages": 1}
}
```

- `status` is one of:
  - `unused`: not redeemed yet; can be revoked.
  - `used`: redeemed; `used_at` is set (only for this status).
  - `revoked`: revoked and refunded; `revoked_at` is set.
  - `unknown`: an active purchase whose code is neither unused nor used, for
    example expired by hand in the database. The server logs it; the user is
    told to contact support. It is never shown as a normal state.
- The item is an explicit allowlist. It never contains who redeemed the code,
  the notes, the `request_id` or user objects.
- `remaining_amount_24h` and `remaining_count_24h` are the caps minus the
  last 24 h of purchases (database clock), floored at 0. They are a display
  snapshot; the purchase checks again under the lock.

### `POST /api/v1/redeem/purchases`

Request: `{"amount": 10.5, "request_id": "<UUID>"}`. Response: the purchase
item plus `"replayed": true|false`.

- `amount` is a JSON number, or a string holding a valid JSON number. A
  malformed numeric string such as `"+10"` or `" 10 "` fails JSON binding
  with a plain 400 without `reason`. A missing or `null` amount passes binding
  as an empty value and is rejected by the check below. The number must match
  `^[0-9]+(\.[0-9]{1,2})?$`, be at most 32 characters and be above 0:
  `10.500`, `1e2` and negative values are rejected with 400
  `REDEEM_PURCHASE_INVALID_AMOUNT`, never rounded. The parsed decimal is the
  only value used for every column and comparison.
- `request_id` is the idempotency key: a canonical 36-character UUID in any
  letter case, stored in lower case. The nil UUID is rejected (400
  `REDEEM_PURCHASE_INVALID_REQUEST_ID`). Use one `request_id` per purchase
  intent, and reuse it when retrying.
- After JSON binding, `request_id` is checked before `amount`. Both checks run
  before any database access.

The purchase then runs in one transaction, in this order:

1. Lock the buyer row (`SELECT … FOR UPDATE`). The buyer must be active (403
   `USER_NOT_ACTIVE`).
2. **Replay check** by (buyer, `request_id`), a plain read. If a purchase
   exists with the same amount, it is returned with its current status (even
   `revoked` or `used`) and `replayed: true`. Nothing is charged again. If it
   exists with a different amount, the answer is 409
   `REDEEM_PURCHASE_REQUEST_CONFLICT`. Because this check runs before the
   switch and the limits, a retry still gets its purchase after a switch-off
   or a cap change.
3. Read the settings (see above).
4. Switch off: 403 `REDEEM_PURCHASE_DISABLED`.
5. Amount below the minimum or above the maximum: 400
   `REDEEM_PURCHASE_AMOUNT_BELOW_MIN` / `REDEEM_PURCHASE_AMOUNT_ABOVE_MAX`.
6. Rolling 24 h caps: 429 `REDEEM_PURCHASE_WINDOW_COUNT_LIMIT` /
   `REDEEM_PURCHASE_WINDOW_AMOUNT_LIMIT`. The window ends at the database
   clock read after the lock, so time spent waiting for the lock does not
   make a purchase look older.
7. Balance (the database value read under the lock) below the amount: 400
   `INSUFFICIENT_BALANCE`.
8. Write: deduct the balance (the `UPDATE` also requires
   `balance >= amount`), insert the unused `balance` code, insert the
   `redeem_code_purchases` row, set the code's notes, insert the
   `purchase_debit` record, commit.

Concurrent requests:

- A unique violation on (buyer, `request_id`) means a concurrent duplicate
  committed first. The transaction is rolled back, the purchase is read again
  and answered by the replay rule. Exactly one purchase and one charge exist.
- Any other unique violation (a generated code collision) retries the whole
  transaction once with new codes.

After commit, the buyer's auth cache and then the billing balance cache are
invalidated. Both share one 3 s timeout that does not depend on the request's
cancellation. A failure is only logged.

### `POST /api/v1/redeem/purchases/:id/revoke`

Revokes one of the user's own purchases. Another user's purchase or a
malformed id gives 404 `REDEEM_PURCHASE_NOT_FOUND`. It works with the switch
off. One transaction, locking purchase → code → user:

1. Lock the purchase row. If it is already revoked, it is returned unchanged
   (200). A second revoke never refunds twice.
2. `UPDATE redeem_codes SET status = 'expired' WHERE id = ? AND status =
   'unused'`. If no row changes and the code is used: 409
   `REDEEM_PURCHASE_CODE_USED`. Any other code state: 500
   `REDEEM_PURCHASE_INCONSISTENT` (logged).
3. Refund: add the amount to the buyer's balance. This is a plain add and is
   never refused, even when the balance is negative.
4. Mark the purchase `revoked` and set `revoked_at`.
5. Insert the `purchase_refund` record and commit.

The response is the item with `status: "revoked"`. After a refund the caches
are invalidated as after a purchase. Redeem and revoke both change the code
only `WHERE status = 'unused'`, so for the same code exactly one of them
succeeds.

## Records

Migration `132_redeem_code_purchases.sql` adds `redeem_code_purchases`: one
row per purchase with `buyer_user_id` and `redeem_code_id` (both foreign keys
`ON DELETE RESTRICT`; `redeem_code_id` unique), `amount DECIMAL(20,8) > 0`,
`request_id` (`UNIQUE (buyer_user_id, request_id)`), `status` `active` or
`revoked` with `revoked_at` set exactly when revoked, and `created_at`.

Each purchase also writes rows in `redeem_codes`:

| Row | `type` | `value` | `status` | `used_by` | `notes` |
|---|---|---|---|---|---|
| The bought code | `balance` | +amount | `unused`, then `used` (redeemed) or `expired` (revoked) | the redeemer | `user purchase #<id>` |
| Debit record | `purchase_debit` | −amount | `used` | the buyer | `user purchase #<id>` |
| Refund record (revoke only) | `purchase_refund` | +amount | `used` | the buyer | `user purchase #<id> revoked` |

The records have a random 32-hex `code` and `used_at` set to the purchase or
revoke time. They are `used`, so they can never be redeemed, and their types
are not in the admin generate whitelist. The purchase, the bought code and the
debit record share one `created_at`, the database clock read after the lock.

Where the records appear:

- User redeem history (`GET /api/v1/redeem/history`, the user's 25 latest
  `redeem_codes` rows as `used_by`): the buyer sees the debit and refund
  records; the redeemer sees the bought code as an ordinary balance
  redemption. ccyou.dev `/billing` labels them 购买兑换码 −$X and 撤回退款 +$X.
- Admin balance history (`GET /api/v1/admin/users/:id/balance-history`;
  classic console 用户管理 → 充值记录) with type filters 购买兑换码（扣余额） and
  撤回已购兑换码（退款）.
- Admin redeem code list (classic console 兑换码), which shows the bought
  codes and the records, with type filters for both record types.

## Integrity rules

- Admin delete (`DELETE /api/v1/admin/redeem-codes/:id`), batch delete
  (`POST …/batch-delete`) and expire (`POST …/:id/expire`) refuse a bought code
  (active or revoked purchase, checked in the database) and every
  `purchase_debit` / `purchase_refund` record: 400
  `REDEEM_PURCHASE_RECORD_IMMUTABLE`. Batch delete skips these rows, and
  rows whose check fails, and reports only the rows it deleted. The foreign
  keys are the backstop. The same `checkImmutableHistoryRecord` guard still
  protects admin operation records (`ADMIN_RECORD_IMMUTABLE`).
- The classic admin 兑换码 list still shows a delete button on unused bought
  codes; the backend refuses the delete.
- Spending balance on a code is not a top-up, whoever redeems the code:
  - **Invite eligibility** (`HasQualifyingTopUp`, `GET /api/v1/invites` →
    `eligible`): a user qualifies with a completed payment order, a credited
    stablecoin top-up (`crypto_pay_credits`), or a redeemed balance code with
    a positive value, subscription code or concurrency code. Bought codes are
    excluded, whatever their purchase status, so redeeming a code a user bought
    with balance (one's own or another user's) does not qualify. Subscription
    and concurrency codes still qualify.
  - **Total recharged** (总充值, `SumPositiveBalanceByUser`, admin balance
    history `total_recharged`): positive `balance` and `admin_balance` codes
    the user redeemed, excluding bought codes, plus credited stablecoin
    top-ups. Native payment orders are counted through the balance code their
    fulfilment redeems. Debit and refund records are not counted.

## Known limitations

- **In-flight usage can overdraw after a purchase.** The purchase checks the
  database balance under the row lock, but requests already admitted are
  charged when they finish, and the usage deduction has no `balance >= cost`
  guard. A user who spends nearly all of the balance on a code while requests
  are running can end with a negative balance.
- **Billing balance cache back-fill race (pre-existing, also affects redeem).**
  In `BillingCacheService.GetUserBalance`, a request that read the old
  balance from the database before an invalidation can write it back into the
  cache after a purchase, refund or redeem invalidated it. The database
  balance is always correct; the cached copy can stay stale until it expires
  or is invalidated again.
- **Classic user 兑换 page.** Its history labels `purchase_debit` and
  `purchase_refund` rows 未知 / Unknown and does not format their value as
  money. Only ccyou.dev has a purchase UI, but the user API is not limited to
  it: a classic-site user who calls the API directly sees these labels.
- **ccyou.dev keeps the `request_id` in memory only.** After a network error,
  timeout or 5xx the page offers a "retry the same request" button that
  resends the same `request_id`. After a page reload that id is gone; a
  purchase whose response was lost but was charged appears in 我买的兑换码
  after the reload.
- Admin create-and-redeem (`POST /api/v1/admin/redeem-codes/create-and-redeem`)
  with the text of an unused bought code redeems it to the target user like
  any other unused code. This is an existing admin privilege.

## Operator checklist before enabling

Any balance becomes transferable to another account once the switch is on.
Check what a balance unlocks on the site:

1. **Access levels.** `GET /api/v1/admin/access-levels` lists each level's
   `balance_threshold` and `payg_discount_multiplier`. A database trigger on
   `users.balance` sets a user's automatic level to the highest level whose
   threshold is at or below the balance. With
   `access_level_balance_maintenance_enabled` off, it only raises levels, so
   a level never drops when the balance falls. The same money can then raise
   the level of several accounts: top up to a threshold, buy a code for the
   whole balance (the buyer keeps the level), and the redeemer reaches the
   threshold too. A manual level, when set, takes precedence over the
   automatic one.
2. **Groups gated by level.** Check `groups.required_level_id`. A transferred
   balance can satisfy that level requirement on several accounts; the
   group's other access conditions still apply.
3. **Free balance.** `default_balance` for new users, promo-code bonuses and
   admin grants become transferable: free balance collected on several
   accounts can be pooled into one.
4. **Caps.** Set the per-code range and the 24 h amount and count caps to
   match the expected gift size. 0 blocks purchases; it does not lift a cap.
5. **Future earnings.** Today no hosted-sharing earnings reach
   `users.balance` (migration 131 settlement events are records only). If a
   later feature credits earnings to the balance, purchases make them
   transferable. Decide this before such earnings go live.

## Release and rollback

Migration 132 is additive: it creates the new, empty table and its index and
rewrites no existing rows. It runs with `SET LOCAL lock_timeout = '5s'` and
`statement_timeout = '60s'`. The foreign keys take a brief
`SHARE ROW EXCLUSIVE` lock on `users` and `redeem_codes`.

To roll back, switch purchases off first. An older application ignores the new
table, but it does not know about purchases: its admin expire and
debit/refund-record delete have no purchase guard (deleting a bought code is
still blocked by the foreign key), and its invite eligibility and total
recharged count redeemed bought codes.

## Tests

Paths are relative to `backend/internal/`.

- `repository/redeem_purchase_integration_test.go` (`-tags integration`,
  real PostgreSQL): the happy path, balance edges, validation, range and
  switch, window caps with revoked purchases, concurrent purchases and
  duplicate `request_id`s, replay after switch-off and revoke, redeem/revoke
  races, admin delete/batch delete/expire refusals and the foreign-key
  backstop, top-up exclusions, an inactive buyer, a switch-off while queued
  on the lock, and settings read on the transaction connection.
- `service/redeem_purchase_service_test.go` and
  `handler/redeem_purchase_handler_test.go` (`-tags unit`): check order,
  refusals, retries, revoke flow, status mapping, the response allowlist and
  input validation.
- `handler/admin/setting_handler_redeem_purchase_test.go`: defaults,
  keep-previous, validation and the PUT response.
- `repository/top_up_crypto_integration_test.go` (`-tags integration`): the
  stablecoin part of invite eligibility and total recharged.
