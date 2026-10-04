# OpenAI OAuth WebSocket quota failover

This contract applies to admitted official Codex clients using pooled OpenAI
OAuth accounts. It does not change OAuth login, user profiles, conversation
files, user/project configuration, SAIAI Key quotas, or native-relay accounts.

## Signals and account selection

- A provider WebSocket handshake HTTP 429 is recorded as an upstream account
  rate limit. A replay-safe first request can then use another compatible
  account without closing the downstream connection.
- Quota errors are recognized in top-level `error` events and nested
  `response.failed` / failed `response.done` envelopes. Their reset timestamp
  and provider-supplied Codex quota headers are used for local scheduling.
- Official `codex.rate_limits` frames update the account's observed five-hour
  and weekly snapshots. Only the default Codex limit and explicit 300-minute
  or 10080-minute windows are interpreted as account quotas. Unknown and
  model-specific limit families are not converted into account-wide limits.
- A known exhausted window with a valid future reset also prevents the next
  turn on that existing connection from being sent to the exhausted account.
  Missing/invalid reset data does not create a permanent disable or invent a
  reset time; an actual provider error still follows the normal error policy.
- Exhausted accounts are excluded from replacement selection. Existing
  bounded retries, model/transport compatibility, concurrency, moderation,
  user balance and SAIAI Key quota checks remain in force.
- When both windows are exhausted, use the latest valid future reset across
  both windows. An already reset weekly window must not hide an active
  five-hour limit; this rule is aligned for snapshots, cached account fields,
  and HTTP/WS 429 header handling.
- After a valid live quota frame, internal turn results no longer reuse old
  handshake quota headers to overwrite that newer observation. Other
  handshake metadata is retained, and the wire quota frame is unchanged.

## Replay boundary

Quota telemetry alone is not generated turn output. Empty `response.created`
and `response.in_progress` frames may be held briefly until the first output
or terminal event. This option is enabled only on the official OAuth
failover path. Normal successful frames retain their message type, bytes,
and relative order; failed-attempt start frames are not exposed before retry.

The start buffer is bounded to eight frames and 64 KiB. Unknown events,
binary frames, nonempty output, or either buffer bound cause forwarding and
commit that attempt. Once any turn output has been forwarded, or a failed
terminal reports output/tokens, the Gateway does not replay the turn. This
avoids duplicating visible replies, tool calls or usage.

The resumable downstream writer belongs to the ingress connection, not an
individual upstream attempt. Rotating an exhausted account does not cancel an
in-flight downstream write and accidentally close the user's socket. Ingress
cancellation and the configured write deadline still apply.

Continuation replay requires a complete process-local history for that
user/response pair. It removes account-bound `previous_response_id` and
encrypted reasoning data only on cross-account replay, while reconstructing
the known input sequence. Missing history, provider item references, or
ambiguous pipelined turns fail closed. A pool without a compatible replacement
returns a retryable failure; it does not loop indefinitely or change the
user's OAuth/profile state.

## Displayed plan and limits

The local proxy's synthetic login identity is not the selected upstream
account identity. A placeholder Plus label therefore does not prove the
selected account's subscription. Provider quota snapshots and plan metadata
belong to the selected upstream connection and may be cached by an official
client; they are not the SAIAI Key balance or aggregate pool capacity.

This change observes quota frames without falsifying or removing them. A
replacement account's own frames are forwarded normally. Display filtering,
neutral placeholder plans, and native CLI/Desktop cache refresh require their
own compatibility checks; passing these Gateway mocks does not certify those
UI behaviors.

## Validation scope

The quota matrix uses local WebSocket clients and mock upstreams only. It
covers five-hour/weekly limits, quota control frames, empty start frames,
nested failure envelopes, a quota-exhausted next turn, handshake 429, partial
text/tool output, unavailable replacements, and unavailable replay history.
Relay tests additionally check byte ordering and bounded-buffer fail-closed
behavior. No real provider/model requests are made by these tests.

The observed quota frame shape is checked against official Codex
`rust-v0.153.4` source in `codex-api/src/rate_limits.rs` and
`codex-api/src/endpoint/responses_websocket.rs`. An actual official client or
Desktop renderer, live pooled OAuth traffic, native history/MCP preservation,
and deployment acceptance are separate validation stages.
