# Codex local API Key quota errors

When a SAIAI API Key's total quota is exhausted, Responses requests return
HTTP **403** with a readable OpenAI error envelope:

```json
{
  "code": "API_KEY_QUOTA_EXHAUSTED",
  "message": "API key 额度已用完，请联系管理员调整额度或更换 API key。",
  "error": {
    "code": "API_KEY_QUOTA_EXHAUSTED",
    "type": "permission_error",
    "message": "API key 额度已用完，请联系管理员调整额度或更换 API key。"
  }
}
```

This applies to both stored `quota_exhausted` status and an active Key whose
`quota_used >= quota`, on `/v1/responses`, `/responses`, and their subpaths
(including `/compact`). GET WebSocket handshakes use the same rejection before
upgrading. No model handler or upstream account selection is reached. The top
level fields remain available to existing consumers and Ops attribution.
Authenticated `/v1/usage` remains accessible to exhausted Keys.

Other protocols retain their existing quota responses. Subscription window
limits, moderation cooldowns, provider rate limits, and other temporary limits
are unchanged; their 429 status must not be converted by this rule. This is a
local Key quota rejection, not an upstream ChatGPT subscription quota event.

## Why the status and envelope both matter

Official Codex maps an unrecognized HTTP 429 to `RetryLimit`, replacing the
provider message with `exceeded retry limit, last status: 429 Too Many Requests`.
Simply adding a message to that 429 does not fix its display. A generic 403
preserves the response body; `error.message` lets Codex extract readable text
instead of showing JSON. The previous flat `code`/`message` body did not provide
this OpenAI field.

These behaviors were checked against the official
[Codex 0.162.1 API error mapping](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/codex-api/src/api_bridge.rs)
and [error display](https://github.com/openai/codex/blob/rust-v0.162.1/codex-rs/protocol/src/error.rs).
Codex still performs bounded stream reconnections for generic 403 errors and
adds its own English prefix and request URL. This change preserves the quota
message in the final error; it does not disable Codex's stream retry policy or
customize its UI.

## Validation

The middleware regression suite covers persisted/runtime quota exhaustion,
HTTP Responses and compact aliases, WebSocket upgrade rejection, retained legacy
responses, usage lookup, and subscription window 429s.

An optional official-client test starts the real Codex executable with a clean
temporary home and connects it directly to the modified Gateway middleware on
loopback. It supplies only a synthetic test Key, and checks the final failed-turn
message. Run with a separately verified official executable:

```bash
cd backend
SAIAI_CODEX_QUOTA_TEST_BINARY=/path/to/verified/codex \
  GOMAXPROCS=2 go test -p 1 -tags=unit ./internal/server/middleware \
  -run TestCodexOfficialClientReadsGatewayQuotaMessage -count=1
```

Linux official Codex **0.159.2** and **0.162.1** passed ordinary-turn probes
against the middleware. Separate loopback mock probes using their actual
app-server binaries seeded a synthetic turn, then invoked `thread/compact/start`.
Both versions reproduced the old 429 message and retained the new Chinese
message after the final failed compaction. These probes used one configured
stream retry and observed two quota-rejected attempts for 403; no provider or
live credentials were used.

This evidence covers Linux CLI and the app-server error notification consumed
by IDE/Desktop clients. The rendered IDE/Desktop UI, Windows/macOS binaries,
and an established WebSocket session were not exercised by these probes.
