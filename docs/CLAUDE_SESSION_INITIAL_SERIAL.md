# Claude session startup serialization

Official Claude Code `/v1/messages` requests routed to Anthropic OAuth or
setup-token accounts serialize their first **three forwarding attempts** after
initial account selection or an account switch. The counter is scoped to the
group and existing sticky session key. It spans models and includes tool-result
turns and auxiliary requests; it does not infer that their cache prefixes match.
Intercepted requests and `/v1/messages/count_tokens` do not consume the counter.
Other clients, account types, and requests without a metadata session ID keep
their existing behavior.

For streams, the next attempt starts after a parsed `message_start` containing a
message object. HTTP response headers and SSE pings do not release the lease.
The preceding response may continue streaming. For non-streaming requests, the
lease is released after response-body processing. A failed forwarding attempt
also releases the lease and counts toward three. A reservation canceled before
forwarding is refunded. Internal same-account retries within one forwarding
attempt consume only one position. The fourth request waits for the third to
release before resuming normal concurrency. Concurrent arrivals are ordered by lease acquisition,
not by client timestamps.

Redis stores only a hashed session key, account ID, generation, counter, and
request lease. Switching accounts resets the counter, including A → B → A.
Waiting requests from an earlier generation reselect; old response callbacks
cannot release a new generation's lease. The state expires after one hour of
inactivity, so a returning session after that interval starts another three
attempts. A lease expires after at least two minutes to recover crashed owners.
An expired owner's reserved attempt remains counted.

Waiting retains the user's concurrency slot to bound queue size, but releases
the selected account slot; an account slot is reacquired before forwarding.
Requests continue through existing concurrency limits, account-level message
queues, admission checks, billing, failover, and success-only sticky confirmation.
Starting a response never confirms a long-lived sticky binding.

Configuration (enabled by default):

```yaml
gateway:
  claude_session_initial_serial:
    enabled: true
    wait_timeout_ms: 90000
```

`GATEWAY_CLAUDE_SESSION_INITIAL_SERIAL_ENABLED=false` disables the feature.
The count is fixed at three. The wait deadline is shared across this request's
selection and failover loop. The timeout accepts 0–300000 milliseconds; zero
uses the 90-second default. Redis failures and wait expiry log
`gateway.session_initial_serial_bypassed` and allow normal forwarding. Client
cancellation or a failed downstream keepalive write stops waiting and does not
forward. These bounded fallback paths can still produce duplicate cache writes.
Existing account-level `serialize` queues remain effective after the first three;
disabling that independent queue
is required if subsequent requests should have no extra account-level serialization.

This reduces concurrent cold-cache writes only when requests share a cacheable
prefix in the same upstream cache scope. It does not guarantee cache hits or
detect cache readiness. Subsequent requests gain waiting latency, especially
when the initial three requests use different models or prefixes.
