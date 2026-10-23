# Codex (ChatGPT OAuth) TLS fingerprint

OpenAI OAuth accounts can opt in to reproducing the **initial, non-resumed
ClientHello** of the official `codex-cli 0.163.0` Linux x86_64 build. The
official client uses two different TLS stacks, so there are two pinned
baselines, selected by connection role — never by the request User-Agent:

| Baseline | Role | Official stack | Used for |
| --- | --- | --- | --- |
| `codex-0.163.0-linux-ws-rustls` | `ws` | rustls 0.23.45 (aws-lc-rs) | Responses WebSocket model connection |
| `codex-0.163.0-linux-http-openssl` | `http` | reqwest native-tls, vendored OpenSSL 3 (`openssl-src 300.6.1+3.6.3`) | HTTP/SSE model requests and all control-plane HTTPS |

This mirrors the Claude feature ([CLAUDE_TLS_FINGERPRINT.md](CLAUDE_TLS_FINGERPRINT.md))
but is a separate, explicit transport identity. Claude behavior is unchanged.

## Enablement

- Account flag: `extra.enable_tls_fingerprint`. For **OpenAI OAuth** accounts
  it is honored **only when explicitly `true`** (default off). Anthropic
  OAuth/setup-token accounts keep their default-on semantics. OpenAI API-key
  and Codex native-relay accounts never use it, even if the key is present.
- Global switch: `gateway.tls_fingerprint.enabled` (default true) gates HTTP
  **and** WebSocket. With it off, opted-in accounts use standard Go TLS.
- WebUI: Create/Edit account modals show "Codex TLS fingerprint simulation" in
  the OpenAI OAuth section. It is independent of the Anthropic toggle, defaults
  to off, and turning it off on an account that had it on writes an explicit
  `false`. The account DTO exposes `enable_tls_fingerprint=true` for opted-in
  OpenAI OAuth accounts (previously the field was only mapped for Anthropic,
  which made the edit modal show "off" and overwrite).
- Toggling takes effect for new connections: HTTP uses a separate client pool
  per policy; WebSocket connections are tagged with their policy (see below).

## Where the policy applies

When enabled, these requests use the pinned Codex identity for their role:

| Request | Code | Role |
| --- | --- | --- |
| Codex `/backend-api/codex/responses` HTTP/SSE (OAuth) | `OpenAIGatewayService.Forward` HTTP path | http |
| Native Codex images `/backend-api/codex/images/*` (official-client-only path) | `ForwardNativeCodexImage` | http |
| Codex models manifest `/backend-api/codex/models` (OAuth branch) | `fetchCodexModelsManifestUpstream` | http |
| Admin account test (OpenAI OAuth `/codex/responses`) | `AccountTestService.testOpenAIAccountConnection` | http |
| Background Codex usage probe (`/codex/responses`) | `AccountUsageService.probeOpenAICodexSnapshot` | http |
| Codex quota reset control plane (`/backend-api/wham/*`) | `OpenAIQuotaResetService.request` | http |
| Responses WebSocket: pool acquire, prewarm, ingress session, v2 passthrough | `openAIWSConnPool`, `openai_ws_forwarder.go`, `openai_ws_v2_passthrough_adapter.go` | ws |

Deliberately **not** covered (they are not codex-cli traffic or not on a
shared transport):

- ChatGPT conversation/Desktop/attestation/updates paths
  (`ForwardChatGPTConversation`, `ForwardChatGPTControl`,
  `ForwardChatGPTDeliveryControl`, chat-updates WebSocket): a different client
  (ChatGPT app), whose TLS shape was not captured.
- Public Images API adapter (`openai_images_service.go`): Responses conversion,
  not an official-client wire path.
- OAuth token refresh to `auth.openai.com`, `openai_privacy_service.go`: they
  use dedicated clients outside the account upstream pool. Follow-up candidate.

## Transport design

- `tlsfingerprint.TransportPolicy{Family, Baseline, Role}` is an explicit,
  immutable value chosen by the caller from the request purpose
  (`codexTransportPolicyFor(cfg, account, role)`). The zero value means
  standard Go TLS. Unknown baselines or family/role mismatches fail; a
  non-zero policy against an upstream/dialer that cannot honor it returns
  `ErrTransportPolicyUnsupported`. **There is no silent fallback to standard
  TLS** and no re-dial with a different stack after a handshake failure.
- `tlsfingerprint.PolicyDialer` performs direct, HTTP CONNECT, HTTPS CONNECT
  (verified outer TLS) and SOCKS5/SOCKS5H (context-aware) tunnelling itself;
  `http.Transport.Proxy` stays nil so Go never bypasses it. One deadline
  (handshake timeout or context deadline, whichever is earlier) covers CONNECT
  plus TLS; context cancellation closes the socket; deadlines are cleared after
  the handshake. Target certificates are always verified (system roots plus
  `SAIAI_EXTRA_CA_*`). IP-literal targets omit SNI, as both official stacks do.
- Each connection gets a fresh `ClientHelloSpec` (uTLS `ApplyPreset` mutates
  extension and key-share objects), a fresh rustls order seed and fresh keys.
  No session cache: no tickets/PSK are ever offered.
- HTTP: `httpUpstreamService.DoWithTransportPolicy` shares one execution path
  (`executeOnEntry`) with `Do` and the Claude `DoWithTLS`, so the
  native-Codex no-redirect policy, in-flight accounting and response tracking
  are identical. The attempt-budget helper (`doOpenAIUpstreamWithPolicy`) keeps
  reservation, redirect context and `GetBody=nil` semantics for every policy.
  Policy transports are HTTP/1.1-only (`TLSNextProto` empty, no ALPN), refuse
  plain-HTTP dials, and get the bounded `account_aux_connection_reserve` so
  control requests do not queue behind open SSE streams (the reserve is now
  tied to H1 pool semantics, not to the fingerprint flag; Claude pools are
  unchanged). Client cache keys include the policy key.
- WebSocket: `coderOpenAIWSClientDialer.DialWithPolicy` uses a cached
  `http.Client` per (policy, proxy) whose transport has `Proxy=nil`,
  `DialTLSContext` = policy dialer, no HTTP/2 and `DisableKeepAlives` (one TLS
  connection per upgrade, like the official client; failed upgrades never
  leave pooled connections another account could reuse). The 101 response
  body remains the raw `io.ReadWriteCloser` coder/websocket needs; redirect
  policy, 429 header-receipt timing, read limit and tracing are unchanged.
  These clients are excluded from the proxy-client cache metrics
  (`TransportReuseRatio` still means standard proxy-client cache hit ratio).
- WebSocket pool: every upgraded connection records `transportKey`
  (policy key, `""` = standard). Acquire only hands out connections whose key
  matches the request (preferred, least-busy, scan and queue-wait paths);
  at capacity an idle mismatched connection is evicted to make room; cleanup
  drains idle, unpinned, waiter-free mismatched connections. Forced-preferred
  (continuation-bound) acquisitions keep their existing connection after a
  toggle and pinned connections are not drained — they are never migrated to
  the new transport mid-chain. Prewarm reuses the last acquire request,
  including its policy.

## Baseline details

Both baselines: record version `0x0301`, legacy version `0x0303`, 32-byte
session id, null compression only, no GREASE, no ALPN, no padding, no ECH,
no PSK, `supported_versions` = TLS 1.3, TLS 1.2, `psk_key_exchange_modes` =
psk_dhe_ke, `key_share` = X25519MLKEM768 (1216 bytes) + X25519 (32 bytes).

### `codex-0.163.0-linux-ws-rustls` (1468-byte ClientHello for `chatgpt.com`)

- Cipher suites (10): `1302 1301 1303 c02c c02b cca9 c030 c02f cca8 00ff`
  (renegotiation SCSV, no `ff01` extension).
- Extensions: `server_name, status_request, supported_groups(11ec 001d 0017 0018),
  ec_point_formats(00), signature_algorithms(0503 0403 0603 0807 0806 0805 0804
  0601 0501 0401 0904 0905 0906), extended_master_secret, session_ticket(empty),
  supported_versions, psk_key_exchange_modes, key_share`.
- Order: rustls 0.23.45 `order_insensitive_extensions_in_random_order` — a
  per-connection random u16 seed; extensions sorted (stable, declaration
  order) by `low_quality_integer_hash((seed << 16) | ext_type)`
  (`rustls/src/msgs/handshake.rs`). `TestRustlsExtensionOrderReproducesEveryCapturedOrder`
  finds a seed for each of the 12 captured orders (seeds 6471, 46322, 63940,
  52346, 29202, 33671, 42779, 27690, 30061, 43778, 9791, 20026).
- The standalone X25519 share **reuses** the X25519 component of the hybrid
  share (bytes 1184..1216), as rustls does. Implemented: the X25519 share data
  and the uTLS X25519 private-key state both point at the hybrid key, so a
  server selecting plain X25519 completes the handshake.
- JA4 `t13d101000_61a7ad8aa9b6_0d308c48d2a3`; JA3n (sorted) `54294ea073f85fb8975a09107e5dd489`.

### `codex-0.163.0-linux-http-openssl` (1542-byte ClientHello for `chatgpt.com`)

- Cipher suites (30), OpenSSL order: `1302 1303 1301 c02c c030 009f cca9 cca8
  ccaa c02b c02f 009e c024 c028 006b c023 c027 0067 c00a c014 0039 c009 c013
  0033 009d 009c 003d 003c 0035 002f`.
- Fixed extension order: `renegotiation_info(00), server_name, ec_point_formats(00),
  supported_groups(11ec 001d 0017 001e 0018 0019 0100 0101), session_ticket,
  encrypt_then_mac, extended_master_secret, signature_algorithms(26 schemes,
  ML-DSA first), supported_versions, psk_key_exchange_modes, key_share`.
- Independent X25519 key (OpenSSL does not share it), as captured.
- JA4 `t13d301100_1d37bd780c83_8e6e362c5eac`; JA3 `0b85eb0d4981e69064e40753e4f0ac5f`.

Fixtures: `backend/internal/pkg/tlsfingerprint/testdata/codex-0.163.0-*.{json,clienthello.bin}`.
Tests compare every wire byte except random, session id and key material
against the raw captures (WS with the capture's seed for an exact order match).

## Provenance

- Binary: official `codex-cli 0.163.0` standalone package
  `0.163.0-x86_64-unknown-linux-musl` (sha256 `399ff69c…c4215`), Linux x86_64.
- Captured 2026-10-23 with a loopback-only harness (`/workspace/tmp/codex-tls`):
  `HTTPS_PROXY` → Go CONNECT front that records the raw ClientHello and
  terminates TLS with a local CA → local mock. Never connected upstream.
- Default CA environment (no `CODEX_CA_CERTIFICATE`/`SSL_CERT_FILE`; local CA
  trusted via `SSL_CERT_DIR` only): 6 runs (3 with WebSocket accepted, 3 with
  WebSocket rejected → HTTP/SSE fallback), 123 connections: 6 model WebSocket
  (shape A) and 117 HTTP/SSE + control-plane (shape B, including
  `ab.chatgpt.com`). The WebSocket stack is rustls regardless of CA mode; the 6
  WebSocket connections of the custom-CA runs had the identical shape, giving
  12 shape-A samples / 12 distinct orders. Apart from the WS order seed, every
  sample of a shape was structurally identical.
- Server side (Go `crypto/tls` front): all negotiated TLS 1.3,
  X25519MLKEM768, no ALPN, not resumed.
- **Not targets:** the custom-CA variants (`CODEX_CA_CERTIFICATE` set switches
  reqwest to rustls with `h2,http/1.1` ALPN); macOS and Windows builds.

## Residual differences (known, accepted for v1)

1. **Advertised but not negotiable algorithms.** uTLS can serialize ML-DSA and
   ed448/brainpool/DSA/SHA-224 signature schemes, x448/ffdhe groups, DHE and
   CBC/ETM suites, but cannot use them. A server that selects one fails the
   handshake loudly (tested: HRR to x448/ffdhe2048, TLS 1.2 DHE suite); there
   is no fallback. `chatgpt.com` (TLS 1.3, X25519MLKEM768) never selects them.
   HRR to P-256/P-384 works.
2. **HRR cookie position.** On a HelloRetryRequest with a cookie, uTLS inserts
   the `cookie` extension at a random position; rustls places it by the same
   seeded hash. The rest of the retried hello keeps the original order
   (tested). Go/Cloudflare-style HRRs without cookies are unaffected.
3. **No resumption.** Official rustls resumes later connections with PSK, and
   the simulation always sends the first-connection shape. (OpenSSL/native-tls
   did not resume in any capture.)
4. **Post-handshake behavior** (record sizes, CCS timing, alerts, HTTP/1.1
   header order) is Go's, not rustls/OpenSSL's. Headers are a separate concern.
5. **Platforms/environments**: only Linux, default-CA. Custom-CA, macOS and
   Windows shapes are out of scope.
6. Requests listed as "not covered" above still use standard Go TLS.

## Reproducing the evidence

```bash
cd /workspace/tmp/codex-tls && scripts/stack.sh start gw-e2e routes.json
cd <worktree>/backend
export CODEX_TLS_CAPTURE_PROXY=http://127.0.0.1:18900 CODEX_TLS_CAPTURE_CA=/workspace/tmp/codex-tls/ca/ca.pem
go test ./internal/repository -run TestCodexCaptureHarnessHTTP -count=1 -v   # real HTTP policy path
go test ./internal/service    -run TestCodexCaptureHarnessWS   -count=1 -v   # real WS dialer
cd /workspace/tmp/codex-tls && scripts/stack.sh stop
python3 -I scripts/parse_ch.py cap/gw-e2e/conn*.bin   # compare ja4/ja4_r/ja3n with out/*.json
```

Result (2026-10-23): 6/6 HTTP and 12/12 WS gateway connections match the real
captures on JA4, JA4_r, JA3n, cipher list, ClientHello length, every
extension's bytes (except key material), key-share groups/lengths and X25519
key sharing; HTTP order is identical, all 12 WS orders are distinct rustls-seed
orders; the front negotiated TLS 1.3 / X25519MLKEM768 / no ALPN / not resumed.

## Updating the baselines

A new codex release may change either stack. Re-run the capture harness
against the new binary, add new pinned baselines (new names; never edit a
pinned one), add fixtures and tests, and switch `CodexHTTPTransportPolicy` /
`CodexWSTransportPolicy` to the new revision. The policy key includes the
baseline, so pools and WebSocket connections roll over automatically.
