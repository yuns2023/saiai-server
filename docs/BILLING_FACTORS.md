# User billing factors

For token, image, and media usage, `total_cost` is the base cost computed from
recorded usage and the applicable model or media price. Balance-billed
`actual_cost` is:

```text
total_cost × effective group/user rate × group billed-model rate × account payg discount
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
The account payg discount is between `0` and `1`, defaults to `1`, and applies
only to balance billing. Both configured factors allow up to four decimal
places. A zero factor makes that component free. These factors
do not change account cost (`total_cost × account_rate_multiplier`) or
subscription window usage, which continues to use `total_cost`.

Usage logs retain the effective group/user rate and snapshots of the model
rate and payg discount. Older rows read as `1` for both new factors. Changing
an account or group later cannot change the explanation of a past bill.

The existing configured model-pricing aliases remain authoritative. In
particular, a configured Fable 5.1 → Fable 5 alias continues to use the Fable 5
price for every token category. The new account discount and group model rate
are separate user-billing factors and do not change that base price mapping.
