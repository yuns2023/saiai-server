# Codex 0.154.0 Responses metadata at a local mock

## Claim and scope

- Direction: official Codex CLI -> loopback custom Responses provider.
- Surface: Linux CLI `codex exec`; transport: HTTP.
- Authentication: a synthetic environment value accepted only by the local
  mock. No OAuth token, SAIAI Key, or provider credential was used.
- Excluded: SAIAI local proxy and Gateway hops, OAuth mode, WebSocket, Desktop,
  IDE, provider acceptance, and account switching.

## Exact version and isolation

- Official executable: `codex-cli 0.154.0`.
- Isolated `HOME` and `CODEX_HOME` on the workspace filesystem.
- Custom provider: loopback HTTP `/v1/responses`, Responses wire API, no
  WebSocket support, and zero configured request/stream retries.
- The mock parsed two requests in memory, printed only field names, equality
  results, and UUID versions, and returned a synthetic 400 response. It did not
  persist headers or bodies.
- Actual provider model requests: **0**. Local model-shaped mock requests: **2**.

## Observations

| Field or relation | Observed result |
|---|---|
| `client_metadata` keys | `session_id`, `thread_id`, `turn_id`, `root_turn_id`, `x-codex-window-id`, `x-codex-installation-id`, `x-codex-turn-metadata` |
| Flat vs nested turn metadata | All six corresponding identity values matched in the second capture |
| `x-codex-turn-metadata` direct header vs body string | Present and byte-equal in both captured requests |
| `thread-id` and `x-codex-window-id` headers | Present and equal to the corresponding flat body values |
| Direct `session_id` header | Absent on this custom-provider path |
| UUID versions | Session, thread, turn, and root turn: v7; installation: v4; window ID: not parseable as a UUID |
| HTTP response and usage | Mock returned 400; no completed response or usage record |

The [official metadata implementation](https://github.com/openai/codex/blob/main/codex-rs/core/src/responses_metadata.rs)
uses the full turn metadata in `client_metadata` and a bounded compatibility
projection in the direct header. Thus the observed byte equality does not
establish a permanent equality rule; tool-namespace metadata can make the
body string richer than the header.

## Conclusion

This directly proves the listed fields and relationships for this executable
and custom-provider HTTP path. It does not prove the OAuth/local-proxy/Gateway
request shape or whether OpenAI uses any particular metadata ID for account
state. The account-switch change therefore scopes only the established
provider-facing `session_id`/`conversation_id` headers and account-owned
response/turn-state tokens. The official Codex body remains unchanged pending
an isolated OAuth egress comparison.
