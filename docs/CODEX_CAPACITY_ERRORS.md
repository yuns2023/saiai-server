# Codex upstream capacity errors

The provider subscription belongs to the selected upstream account, not the
SAIAI user. The official Codex client can generate its own upgrade/credit
purchase message when it receives `usage_limit_reached`, even when a Gateway
has replaced the error message. Therefore changing only message text is not
sufficient. The official `rust-v0.162.1` `codex-api/src/api_bridge.rs` and
`protocol/src/error_tests.rs` fixtures establish this behavior.

For OpenAI OAuth and native-relay accounts, downstream terminal errors with
`usage_limit_reached` or `insufficient_quota` are mapped to `server_error`, code
`saiai_upstream_capacity_unavailable`, with a SAIAI capacity message. The
replacement error does not include the upstream plan or reset/credit metadata.
For terminal HTTP errors the downstream status is 503, rather than 429: Codex
otherwise replaces the message with its generic retry-limit error. SSE and
WebSocket transports retain their existing transport status and event envelope.
This applies to HTTP error bodies, SSE failures and text/JSON-binary WebSocket
frames. Unrelated errors, output frames and API-key accounts are unchanged.
Response/event identifiers, terminal status and usage outside the error object
are retained. Existing quota-header/event filtering is independent.

Scheduling, rate-limit persistence, failure accounting and safe replay inspect
the original provider error before downstream normalization. Safe early quota
failover remains unchanged; a committed or affinity-bound turn must not be
replayed merely to hide the error. Native relays retain their one-attempt rule,
attempt budget and lack of local account-state mutation; only the explicit
quota-to-capacity downstream status exception above changes. User/key SAIAI billing
quota errors originate in authentication/billing handlers and are not rewritten
by this upstream-only transformation.

Local regression tests use provider fixtures, HTTP/SSE mocks and WebSocket
loopback connections. An isolated official Codex CLI 0.162.1 loopback check also
confirms that the raw 429 produces the upgrade/credit message, normalized 429
hides the explanatory message, and normalized 503 displays the SAIAI capacity
message without an upgrade prompt. This is error-rendering validation, not a
successful model turn or a complete Client/proxy/Gateway integration test.
These checks do not establish acceptance in every installed
official-client version, Desktop or IDE, and send no real model traffic.
