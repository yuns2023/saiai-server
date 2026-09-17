# OpenAI Codex native relay v1

`codex_native_relay_v1` is an explicit upstream protocol for an OpenAI API-key
account whose Base URL points to another SAIAI Gateway. It is not an alias for
the historical `openai_passthrough` boolean and it is not pool mode.

## Configuration

Set the OpenAI API-key account fields as follows:

- `credentials.base_url`: the upstream Gateway URL (not `api.openai.com`)
- `credentials.api_key`: the key accepted by that upstream Gateway
- `extra.openai_upstream_protocol`: `codex_native_relay_v1`

An absent or unknown protocol value resolves to `platform_compat`, preserving
the existing API-key behavior. The old `openai_passthrough` and
`openai_oauth_passthrough` fields do not enable this protocol.

## v1 contract

The ingress request must have the official Codex client shape. Version 1
supports HTTP Responses, SSE, Responses subpaths such as
`/v1/responses/compact`, and the Codex models manifest request. WebSocket
ingress is rejected explicitly.

| Ingress / next hop | Relay v1 | Terminal OAuth | Direct Platform API |
| --- | --- | --- | --- |
| Official Codex HTTP Responses / SSE | Supported | Supported | Not a relay target |
| `/responses/compact` | Supported | Supported | Not a relay target |
| Codex models manifest | Supported | Supported | Not a relay target |
| Responses WebSocket | Rejected in v1 | Existing direct behavior | Existing direct behavior |

“Supported” here describes protocol shape. Account scheduling, quotas, and
billing remain local policy at each Gateway.

For a native-relay hop, the Gateway:

- preserves the request body bytes, including `previous_response_id`, unknown
  fields, and a client-supplied `Content-Encoding` wire representation;
- preserves the Responses path suffix and query string;
- preserves a native Codex models manifest without applying API-key model-list
  conversion or manifest adjustment;
- preserves Codex identity and compatibility headers, including `User-Agent`,
  `originator`, `OpenAI-Beta`, `version`, and `chatgpt-account-id`;
- replaces `Authorization` with the current hop's configured API key;
- removes credentials and transport-owned headers such as cookies, `Host`,
  `Content-Length`, proxy forwarding headers, and hop-by-hop headers; and
- returns an upstream HTTP error without local status remapping, account
  failover, pool retry, or same-account retry.

The terminal OAuth Gateway applies its normal account selection and replaces
`Authorization` and `chatgpt-account-id` with the selected provider account's
values. The internal relay-chain header is removed before the provider request.

Successful SSE is forwarded through the normal Gateway streaming and billing
pipeline. The JSON event semantics are preserved, but v1 does not promise
byte-for-byte response framing because buffering and line endings may be
normalized by that pipeline.

## Chaining and loop prevention

There is no configured or protocol-level hop-count limit. Every relay appends
an opaque, process-local identifier to `X-SAIAI-OpenAI-Relay-Chain`. A Gateway
rejects the request with HTTP 508 if its own identifier is already present.
The identifier contains no hostname, URL, account ID, or deployment name.

Normal HTTP infrastructure still imposes header-size and timeout limits. Those
are transport constraints, not a relay-depth policy.

Each hop makes one upstream attempt. This avoids exponential retry behavior in
a chain. Retry and account-pool policy belongs at the terminal Gateway or the
originating client, not at every relay.

## Compatibility and rollout

All Gateways participating as relay hops must support this contract before the
mode is enabled. Rollback is account-local: switch the upstream protocol back
to `platform_compat`. This change requires no provider request to validate;
local mock tests cover body/header preservation, two relay hops, OAuth
termination, loop rejection, and error behavior.
