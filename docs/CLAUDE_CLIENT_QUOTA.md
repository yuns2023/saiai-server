
## Exclusive-group Claude client quota headers

Claude Messages responses expose the selected OAuth/setup-token account's
upstream unified 5h/7d utilization, reset, status and threshold headers only when
an active user can currently access an active exclusive group with
`show_account_quota` enabled. The selected account must belong to that group.
Authorization is re-read before writing response headers; lookup failures hide
quota, and public groups or revoked grants cannot retain visibility through an
old API-key authentication snapshot. Generic additional response-header settings
cannot bypass this gate. Streaming and non-streaming success paths agree.

These are account meters, not the user's SAIAI balance or the sum of all group
accounts. Values are forwarded without fabrication; absent upstream fields remain
absent. Switching to a public group stops future disclosure but cannot erase a
client's previously received display. Overage and model-specific meters are not
part of this allowlist.

Claude Code 2.1.296 contains status-line consumers for `rate_limits.five_hour`
and `rate_limits.seven_day` (`used_percentage` and `resets_at`). Its `/status`
command describes installation/account/connectivity, while `/usage` uses a
separate OAuth usage endpoint. Header forwarding alone does not implement that
endpoint or promise either command will show percentages. The SAIAI local proxy
already forwards these response headers; it currently does not forward
`/api/oauth/usage`. Local Gateway tests cover the header contract, not a native
client UI acceptance or a live provider request.
