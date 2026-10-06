# Native Codex OAuth request preservation

This contract covers admitted native Codex traffic from Gateway ingress to
the provider request boundary, using an OpenAI OAuth account. It includes HTTP
Responses, compact, model discovery, WebSocket handshake application headers,
and text/binary application frames. It is a development contract; passing local
mocks does not establish official-client or production acceptance.

## Allowed differences

| Boundary | Explicit transformation |
| --- | --- |
| Authentication | Replace Authorization and chatgpt-account-id with the selected provider account. Withhold client Cookie and proxy/API credentials. |
| Destination | Map the Gateway endpoint to the native provider endpoint; preserve application query bytes, duplicates, order, and escaping. |
| Transport | Rebuild Host, lengths, connection framing and WS transport headers; remove hop headers, including Connection-nominated fields, forwarding and proxy headers. Accept-Encoding remains transport-owned. |
| Session isolation | Namespace supplied session/conversation IDs by user and selected account. Preserve each header's name, multiplicity and presence; do not derive absent headers from prompt_cache_key. This is a Gateway pool contract, not an asserted OpenAI requirement. |
| Internal routing | Relay hops append their opaque loop marker; terminal OAuth removes it. |

All other application headers remain client-authored, including User-Agent,
originator, version, OpenAI-Beta, Accept, attestation and future control headers.
Absent Accept/Content-Type remain absent. Model discovery preserves the actual
client identity and query instead of impersonating a fixed CLI version.
Explicit administrative probes without client headers retain their separate
probe defaults.

## Payloads, state and retries

- Preserve the original HTTP wire body, including zstd, whitespace, unknown
  fields, reasoning effort, tools, instructions, encrypted items, and present
  empty/null previous_response_id. A digest guard checks every HTTP attempt.
- Do not recover OAuth invalid_encrypted_content by removing encrypted items.
  Do not recover previous_response_not_found by removing its anchor. Return
  the provider failure so Codex can choose the next request explicitly.
- Existing bounded same-account HTTP transient retries retain the same wire
  body. Existing continuation ownership checks remain mandatory at ingress.
- Forward a supplied opaque turn-state only when it has been observed for this
  user/session and selected account. Preserve its exact value; do not substitute
  the latest cached value or synthesize a missing value. Observe both response
  headers and response.metadata event headers. Concurrent observed tokens can
  coexist until their normal TTL/capacity eviction.
- Unknown, expired, ambiguous or wrong-account turn-state fails closed: HTTP
  409 with turn_state_account_mismatch, or WS policy close. The native WS relay
  validates frame client_metadata before sending that frame as well.
- Quota failover may resend an uncommitted fresh WS frame byte-for-byte. Known
  account-bound anchors, encrypted items, item references, tool outputs and
  turn-state prohibit migration. No sanitized history is reconstructed or
  cached by the native OAuth relay. Once output is committed, do not replay.

Ownership evidence is process-local and bounded. After restart, expiration or
eviction, an old token can require a fresh client conversation. This behavior
is deliberate: silently removing an unverifiable state would send a different
application request. Distributed token ownership is not implemented here.

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

```sh
cd backend
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/service \
  -run '^(Test.*OpenAI|Test.*CodexModels)' -count=1 -timeout=240s
GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/handler \
  -run '^(Test.*OpenAI|Test.*Codex|TestDecodeOpenAIRequestBody_Zstd|TestShouldPreserveOpenAIEncodedWireBody)' \
  -count=1 -timeout=120s
```

This evidence covers Gateway construction/forwarding and local frame relay.
It makes no claim about Luna routing, answer quality, provider acceptance,
actual CLI/Desktop/IDE emission, or a deployed Server/Client pair. Real provider
model and catalog request count in these tests is zero.
