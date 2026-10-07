# Native Codex OAuth request preservation

This contract covers admitted native Codex traffic from Gateway ingress to
the provider request boundary, using an OpenAI OAuth account. It includes HTTP
Responses, compact, model discovery, WebSocket handshake application headers,
and text/binary application frames. It is a development contract; the optional
official-binary proof exercises actual client emission against a local mock.
Neither local proof establishes real-provider or deployed-pair acceptance.

## Allowed differences

| Boundary | Explicit transformation |
| --- | --- |
| Authentication | Replace Authorization and chatgpt-account-id with the selected provider account. Withhold client Cookie and proxy/API credentials. |
| Destination | Map the Gateway endpoint to the native provider endpoint; preserve application query bytes, duplicates, order, and escaping. |
| Transport | Rebuild Host, lengths, connection framing and WS transport headers; remove hop headers, including Connection-nominated fields, forwarding and proxy headers. Accept-Encoding remains transport-owned. |
| Internal routing | Relay hops append their opaque loop marker; terminal OAuth removes it. |

All other application headers remain client-authored, including User-Agent,
originator, version, OpenAI-Beta, Accept, attestation and future control headers.
Absent Accept/Content-Type remain absent. Model discovery preserves the actual
client identity and query instead of impersonating a fixed CLI version.
Session/conversation header names, values, multiplicity and presence remain
client-authored, including underscore and hyphen aliases. Do not namespace
their wire values or derive an absent header from prompt_cache_key. Turn-state
and response ownership keep their user/account scope inside Gateway caches;
preserving wire values does not relax continuation ownership checks.
Explicit administrative probes without client headers retain their separate
probe defaults.
The public `/v1/models` route also recognizes the official client identity when
`client_version` is absent. Its absence must not divert a native request into
the generic model-list handler or synthesize a query parameter.

## Payloads, state and retries

- Preserve the original HTTP wire body, including zstd, whitespace, unknown
  fields, reasoning effort, tools, instructions, encrypted items, and present
  empty/null previous_response_id. A digest guard checks every HTTP attempt.
- Do not recover OAuth invalid_encrypted_content by removing encrypted items.
  Do not recover previous_response_not_found by removing its anchor. Return
  the provider failure so Codex can choose the next request explicitly.
- Existing bounded same-account HTTP transient retries retain the same wire
  body. Existing continuation ownership checks remain mandatory at ingress.
- Native HTTP, models and WS transports reject automatic redirects. A 302/307
  must not create an implicit extra provider request, convert POST to GET or
  resend its body to a new target. HTTP Responses returns the upstream 3xx
  status and payload instead of interpreting it as a completed model turn.
  Per-request policy clones the HTTP client and keeps its shared transport;
  compatibility clients retain their existing redirect policy.
- Forward a supplied opaque turn-state only when it has been observed for this
  user and selected account. Preserve its exact value; do not substitute
  the latest cached value or synthesize a missing value. Observe both response
  headers and response.metadata event headers. Concurrent observed tokens can
  coexist until their normal TTL. A user-scoped token digest and account owner
  are cached through Redis; raw turn-state is never written to Redis. Local
  token entries remain bounded compatibility evidence for in-flight requests.
- Resolve observed turn-state ownership before session-based scheduling. A
  stale session sticky cannot select another account for that token. Response
  ID ownership also applies to native OAuth after HTTP fallback or when WS is
  disabled. Recognize both session_id/session-id and conversation aliases
  without changing the client's header names or inventing an absent header.
- Unknown, expired, ambiguous or wrong-account turn-state fails closed: HTTP
  409 with turn_state_account_mismatch, or WS policy close. The native WS relay
  validates frame client_metadata before sending that frame as well.
- Quota failover may resend an uncommitted fresh WS frame byte-for-byte. Known
  account-bound anchors, encrypted items, item references, tool outputs and
  turn-state prohibit migration. No sanitized history is reconstructed or
  cached by the native OAuth relay. Once output is committed, do not replay.

Shared ownership survives a process restart or another Gateway instance while
the normal TTL and Redis evidence remain valid. Unknown or expired ownership,
including a Redis outage with no local evidence, still requires a fresh
conversation. Silently removing an unverifiable state would send a different
application request. These bindings add no database migration.

## Return path

Native OAuth/native-relay responses preserve safe unknown application headers
and their values, including model/catalog/reasoning metadata and turn-state.
Credential/Cookie, transport and proxy headers are withheld. An enabled explicit
response-header ForceRemove policy remains authoritative.

Native OAuth SSE event payloads keep provider tool names; do not apply OpenCode
tool-name correction. SSE framing/buffering and the existing nonstream SSE-to-JSON
adapter remain distinct response transformations. The Gateway accepts the client
WS connection before selecting/dialing the provider from its first frame, so it
does not retroactively reproduce provider handshake response headers in that
already-issued client 101. Frame metadata remains unchanged.
The local proxy preserves safe application headers and duplicate values from
the Gateway's 101, alongside negotiated subprotocol, and rebuilds only its own
transport response. In the versioned official-binary proof, Codex consumes
response.metadata for WS turn state; a distinct synthetic upstream 101 state
does not alter the later direct-versus-proxy request interaction. This does not
claim provider 101 parity for clients that depend on those handshake headers.

Ordinary OAuth HTTP error envelopes preserve code, param and unknown fields.
An actual selected upstream credential echoed in an error is redacted. Existing
explicit error rules, account-health side effects and pooled 401/402/403 auth
status mapping remain separate administrative boundaries.

## Local evidence

The regression suite uses synthetic credentials, recording HTTP transports,
loopback WS clients and mock upstreams. It checks HTTP/compact JSON and zstd
digests, query/header preservation and absence, model discovery, error fields,
response header boundaries, same-account token ownership, metadata events,
native tool names, two-turn text/binary frames, and byte-identical fresh quota
failover versus rejected continuation migration. API-key compatibility recovery
retains separate tests.

The production HTTPUpstream implementation also has a real loopback TCP test:
JSON/zstd bodies, query and duplicate headers reach its receiver unchanged;
302/307 cause zero follow-up requests. A subsequent compatibility request on
the same pooled client verifies that the policy did not leak to other traffic.
The production WS dialer and models client have separate redirect negatives.

The opt-in official-binary test runs an actual installed Codex CLI and its
app-server through the actual SAIAI executable, using isolated profiles and a
TLS loopback capture that blocks other destinations. It compares capture,
Gateway ingress and mock provider egress: method, endpoint mapping, raw query,
encoded/decompressed body digests, frame type/order/digests, application header
values/presence, including exact session/conversation values, and selected-
account credential replacement. The report pins both executable SHA-256s and
the proof driver SHA-256. Offline negative controls require the comparator to
reject rewritten session values, dropped/overwritten future headers, changed
wire dimensions, missing receipts and incorrect endpoint mapping. The direct mock
baseline separately checks the request interaction sequence, model/reasoning
and exact turn-state fingerprints/response anchors/tool-output presence;
independent client sessions have different
random IDs and their body digests are not claimed to match each other.

This lab uses production request builders/relays but a thin test ingress;
authentication, billing and the public middleware chain are covered separately
by handler/unit tests, rather than a running production DB/Gateway instance.

The public-route proof additionally runs RegisterGatewayRoutes, the actual
API-key authentication middleware, admission, standard-mode balance checks,
account selection, usage recording and billing application. Only persistent
repositories/Redis are in-memory doubles. Production HTTPUpstream, the models
client and the WS dialer use real TCP/TLS through a loopback-only account proxy;
the provider-side HTTP/WS receiver records the bytes it actually receives.
Every accepted non-warmup model payload must have exactly one usage record and
one billing application. Missing authentication and unknown state must reach
no provider request; JSON/zstd compact and Responses bodies are also checked.

```sh
SAIAI_PROOF_CODEX_BINARY=/absolute/path/to/official/codex \
SAIAI_PROOF_CLIENT_BINARY=/absolute/path/to/candidate/saiai \
SAIAI_PROOF_REPORT=/absolute/path/to/sanitized-report.json \
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/service \
  -run '^TestOpenAINativeOAuth_OfficialBinaryLoopback$' -count=1 -timeout=180s
```

Python dependencies: cryptography and zstandard. Optional SAIAI_PROOF_TMPDIR
selects the temporary lab parent. Profiles, synthetic auth, CA/private key and
child process groups are removed on exit. Raw capture remains in memory.

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s backend/internal/service/testdata -p 'test_native_official_codex_probe.py'
```

```sh
cd backend
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/service \
  -run '^(Test.*OpenAI|Test.*CodexModels)' -count=1 -timeout=240s
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/handler \
  -run '^(Test.*OpenAI|Test.*Codex|TestDecodeOpenAIRequestBody_Zstd|TestShouldPreserveOpenAIEncodedWireBody)' \
  -count=1 -timeout=120s
```

The official-binary lab covers Linux CLI and stdio app-server, WS and client-
initiated HTTP fallback, tools and a second app-server turn. It does not cover
Windows/macOS, a Desktop UI or an installed IDE extension, real provider
acceptance, answer quality, Luna routing or a deployed Server/Client pair.
Real provider model and catalog request count in these tests is zero.

Run the public-route proof with the same binary/report variables:

```sh
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/server/routes \
  -run '^TestNativeCodexGatewayRoutes$' -count=1 -timeout=180s
```

Its ordinary synthetic route/transport cases run without the binary variables;
the official-binary subtest skips unless both paths are supplied. The official
probe reports which Gateway fixture was used, avoiding a constructed-request
claim being confused with observed transport bytes. Use a fresh test process
(`-count=1`) for the fixture's ephemeral CA, matching process-wide CA loading.
