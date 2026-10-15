# User billing factors

For token, image, and media usage, `total_cost` is the base cost computed from
recorded usage and the applicable model or media price. Both balance and
subscription billing use `actual_cost`:

```text
total_cost × effective group/user rate × group billed-model rate × effective user discount
```

The group model rate matches the model ID passed to cost calculation
(`billingModel`) case-insensitively. A rule may be an exact ID or a nonempty
prefix ending in `*`: `claude-fable-*=0.6` covers `claude-fable-5`,
`claude-fable-5-1`, and later IDs with that prefix. Exact rules win; otherwise
the longest matching prefix wins. An unmatched model uses `1`. Only a trailing
`*` is accepted; a bare `*` or a wildcard in the middle is rejected. These
rules do not change upstream routing, and `billingModel` can differ from the
displayed upstream model.

The group model rate is between `0` and `100`.
The user discount is between `0` and `1`, defaults to `1`, and applies
to all of that user's API keys, groups, and selected upstream accounts, for both
balance charges and subscription quota consumption. Both configured factors
allow up to four decimal places. A zero factor makes that component free. These factors
do not change account cost (`total_cost × account_rate_multiplier`). Subscription
windows and API-key monetary limits use the same `actual_cost` as balance billing.
A free subscription request consumes no quota and never falls back to the wallet.

Administrators configure the discount on the user create/edit forms. The
legacy `accounts.payg_discount_multiplier` column remains available only for
rollback and historical-data compatibility and does not affect new billing.

Usage logs retain the effective group/user rate and snapshots of the model
rate and effective user discount; compatible API fields retain their `payg`
names. Rows created before migration 095 retain the old account discount
snapshot separately; new rows keep that legacy factor at `1`.
Changing a user or group later cannot change the explanation of a past bill.

The existing configured model-pricing aliases remain authoritative. In
particular, a configured Fable 5.1 → Fable 5 alias continues to use the Fable 5
price for every token category. The user discount and group model rate
are separate user-billing factors and do not change that base price mapping.

## Configuring a subscription model multiplier

In the target subscription group's model rates, set `claude-fable-5-1=2`
(or `claude-fable-*=2` for that family). With an effective group rate and user
discount of 1, a base charge of $0.10 consumes $0.20 of subscription quota.
The same factors on a balance group deduct $0.20 from the wallet. A user-specific
group rate replaces the group default; the model rate then multiplies that rate.
Unit-price overrides remain shared by billed model across groups.

The model-pricing editor simulates the base charge times a model rate in both
billing modes. Group and user factors are fixed at 1 for this model-only estimate
and are not shown as inputs. The simulated model rate is not saved; configure
actual model rates in the group editor and user discounts in the user editor.
Zero model rates are accepted in the preview as well as runtime billing.
User-specific discounts override level discounts rather than stacking with them.
Native Chat retains its separately configured fixed turn/image pricing and
existing rate calculation; its final charge now also consumes subscription quota.
The legacy excess-input long-context calculator retains its surcharge in
`actual_cost` only; that surcharge now applies equally to both billing sources,
while account cost keeps its existing `total_cost` basis.

On deployment, existing non-1 factors begin affecting new subscription requests.
Historical usage, balances and subscription windows are not recalculated or
backfilled. No schema change is needed.

## Administrator usage presentation

The administrator usage UI names the three cost values by their billing role:

- `total_cost` is the **configured base** before user or account multipliers;
- `actual_cost` is **user billed** and drives the cost distribution view; and
- `total_cost × account_rate_multiplier` is **account billed** when an account
  filter makes that aggregate meaningful.

The total-token summary is the sum of input, output, cache-creation, and
cache-read tokens. The UI exposes both cache classes and their share so the
headline total can be reconciled without inspecting individual records.

Administrator dashboard and usage range endpoints accept either inclusive
calendar dates (`start_date` / `end_date`) or a paired, half-open RFC3339 range
(`[start_time, end_time)`). The timestamp pair takes precedence. The “Last 24
hours” preset uses the timestamp form; calendar presets retain local-day
semantics. Cleanup remains calendar-date-only and must not silently widen a
rolling timestamp range.
