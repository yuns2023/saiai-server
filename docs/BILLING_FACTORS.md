# User billing factors

For token, image, and media usage, `total_cost` is the base cost computed from
recorded usage and the applicable model or media price. Balance-billed
`actual_cost` is:

```text
total_cost × effective group/user rate × group billed-model rate × user payg discount
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
The user payg discount is between `0` and `1`, defaults to `1`, and applies
to all of that user's API keys, groups, and selected upstream accounts, but only
to balance billing. Both configured factors allow up to four decimal
places. A zero factor makes that component free. These factors
do not change account cost (`total_cost × account_rate_multiplier`) or
subscription window usage, which continues to use `total_cost`.

Administrators configure the discount on the user create/edit forms. The
legacy `accounts.payg_discount_multiplier` column remains available only for
rollback and historical-data compatibility and does not affect new billing.

Usage logs retain the effective group/user rate and snapshots of the model
rate and user payg discount. Rows created before migration 095 retain the old
account discount snapshot separately; new rows keep that legacy factor at `1`.
Changing a user or group later cannot change the explanation of a past bill.

The existing configured model-pricing aliases remain authoritative. In
particular, a configured Fable 5.1 → Fable 5 alias continues to use the Fable 5
price for every token category. The user discount and group model rate
are separate user-billing factors and do not change that base price mapping.

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
