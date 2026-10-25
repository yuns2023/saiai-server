# Codex (ChatGPT OAuth) TLS fingerprint

OpenAI OAuth accounts can opt in to reproducing the **initial, non-resumed
ClientHello** of the official `codex-cli` build that the gateway presents
itself as. The official client uses two TLS stacks — rustls for the model
WebSocket and reqwest `native-tls` (the OS stack) for HTTP/SSE and the control
plane — and both changed over releases. The baseline is therefore selected by
**connection role** (explicit, chosen by the caller) plus the **version and
platform of the outbound User-Agent** (the UA the gateway actually sends
upstream on that request/dial), like the Claude Code version profiles:

| Baseline | Role | Serves | Official stack |
| --- | --- | --- | --- |
| `codex-0.140.0-ws-rustls-ring` | `ws` | 0.116.0 – 0.140.x, all platforms (and any older UA) | rustls 0.23, *ring* provider |
| `codex-0.155.1-ws-rustls-awslc-pq` | `ws` | 0.141.0 – 0.155.x, all platforms | rustls 0.23.36, aws-lc-rs, X25519MLKEM768 |
| `codex-0.163.0-linux-ws-rustls` | `ws` | ≥ 0.156.0, all platforms; unparsable UA | rustls 0.23.45, aws-lc-rs 1.18 (+ ML-DSA schemes) |
| `codex-0.142.1-linux-http-openssl35` | `http` | Linux < 0.142.2 | native-tls, vendored OpenSSL 3.5 (`openssl-src 300.5.x`) |
| `codex-0.163.0-linux-http-openssl` | `http` | Linux ≥ 0.142.2; unparsable UA | native-tls, vendored OpenSSL 3.6 (`openssl-src 300.6.1+3.6.3`) |
| `codex-0.163.0-macos-http-securetransport` | `http` | macOS, all versions | native-tls → Security.framework (Secure Transport), TLS 1.2 only |
| `codex-0.163.0-windows-http-schannel` | `http` | Windows, all versions | native-tls → SChannel, TLS 1.2 only |

(The WS baseline `codex-0.163.0-linux-ws-rustls` keeps its original name for
pool-key continuity; macOS and Windows send the byte-identical hello.)

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
- Toggling takes effect for newly dialed upstream connections; existing ones
  drain naturally (see "Toggle semantics" below).

## Where the policy applies

When enabled, these requests use the pinned Codex identity for their role:

| Request | Code | Role |
| --- | --- | --- |
| Codex `/backend-api/codex/responses` HTTP/SSE (OAuth) | `OpenAIGatewayService.Forward` HTTP path | http |
| Native Codex images `/backend-api/codex/images/*` (official-client-only path) | `ForwardNativeCodexImage` | http |
| Codex models manifest `/backend-api/codex/models` (OAuth branch) | `fetchCodexModelsManifestUpstream` | http |
| Admin account test (OpenAI OAuth `/codex/responses`) | `AccountTestService.testOpenAIAccountConnection` | http |
| Background Codex usage refresh (`GET /backend-api/wham/usage`) | `AccountUsageService.fetchCodexUsageViaWham` | http |
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

## Baseline selection (version × platform)

`codexTransportPolicyFor(cfg, account, role, outboundUserAgent)` →
`tlsfingerprint.CodexTransportPolicyFor(role, outboundUserAgent)`:

1. Parse the UA with
   `^([^/]+)/(\d+)\.(\d+)\.(\d+)(?:-\S+)? \(([^;)]*); ([^)]+)\)` — the
   codex-rs `get_codex_user_agent()` shape
   `<originator>/<version> (<os> <os version>; <arch>) <terminal> ...`.
   A prerelease (`0.163.0-alpha.10.2`) selects as its base version.
2. Platform: OS field starting with `Mac OS` (or `macOS`) → macOS; `Windows`
   → Windows; anything else (Linux distributions, other Unixes) → Linux.
3. Pick the rule for (role, platform) with the greatest lower bound ≤ the
   version (table above). A WebSocket dial for a version below every WS rule
   (pre-0.116, which never dialed the WebSocket by default) uses the oldest WS
   baseline (`ring`).
4. **Unparsable UA** (no `(os; arch)` part, non-Codex, or the gateway's own
   bare `codex_cli_rs/0.104.0` synthetic UA, `Go-http-client/1.1` when no UA
   is set) → the newest Linux baselines, i.e. exactly the pre-matrix
   behaviour (`CodexHTTPTransportPolicy()` / `CodexWSTransportPolicy()`).

The UA used is the outbound one, after the gateway's own rewriting (account
custom UA, default Codex UA for non-Codex clients, native pass-through):

| Request | Outbound UA read from |
| --- | --- |
| `Forward` HTTP/SSE | the built upstream request (`upstreamReq.Header`) |
| Native Codex images | the built upstream request |
| Models manifest | the outbound `headers` |
| Admin account test | the built request (no UA set → `Go-http-client/1.1` → newest Linux) |
| Usage refresh, quota reset (`codexControlTransport.policyFor(account, req)`) | the built request (synthetic `codex_cli_rs` UA) |
| WebSocket pool acquire / prewarm / v2 passthrough | the upgrade request headers (`openAIWSAcquireRequest.Headers`, passthrough `headers`) |

The policy carries only `{Family, Baseline, Role}`; the raw UA is never part
of a policy, cache key or pool key. Transport/client cache keys use
`policy.Key()` (`codex/<baseline>/<role>`), so requests that select
different **baselines** get separate HTTP pools and WS clients; versions and
platforms that map to the same baseline (e.g. Windows 0.137 and Windows 0.163
HTTP, or every ≥ 0.156 WS client) share one pool. The
WebSocket **connection** pool is per account and, as before, reuses an idle
upstream connection for a later session regardless of the policy it was
dialed with (the same already applies to the upgrade headers, including the
UA, of a reused connection); only new dials follow the current UA.

## Transport design

- `tlsfingerprint.TransportPolicy{Family, Baseline, Role}` is an explicit,
  immutable value chosen by the caller from the request purpose and the
  outbound UA (see above). The zero value means
  standard Go TLS. Unknown baselines or family/role mismatches fail; a
  non-zero policy against an upstream/dialer that cannot honor it returns
  `ErrTransportPolicyUnsupported`. **There is no silent fallback to standard
  TLS** and no re-dial with a different stack after a handshake failure.
- `tlsfingerprint.PolicyDialer` performs direct, HTTP CONNECT, HTTPS CONNECT
  (verified outer TLS) and SOCKS5/SOCKS5H (context-aware, negotiation bounded
  by connect+handshake timeout) tunnelling itself;
  `http.Transport.Proxy` stays nil so Go never bypasses it. One deadline
  (handshake timeout or context deadline, whichever is earlier) covers CONNECT
  plus TLS; context cancellation closes the socket; deadlines are cleared after
  the handshake. Target certificates are always verified (system roots plus
  `SAIAI_EXTRA_CA_*`). IP-literal targets omit SNI, as rustls and OpenSSL do
  (unverified for the macOS/Windows stacks; the upstream is always
  `chatgpt.com`, so this does not arise in practice).
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
- WebSocket pool: the policy is resolved from the account's **current** state
  (scheduler snapshot cache, then repository) at **every new upstream dial** — pool acquire, prewarm, new
  dials from long-lived inbound sessions and v2 passthrough. It is not
  snapshotted in the session's acquire request. Existing upstream connections
  are reused normally whatever policy they were dialed with: there is no
  toggle-time eviction, filtering or queueing change, and continuation-bound
  connections are untouched.

### Toggle semantics

A toggle (account flag or global switch) applies to **newly dialed** upstream
connections only. HTTP: the policy is part of the client cache key, so the
next request uses the other pool; idle connections of the old pool close after
`idle_conn_timeout_seconds` (default 90 s) or when the client is evicted
(`client_idle_ttl_seconds`, default 15 min). WebSocket: existing connections
drain naturally — an idle or released connection is closed by the pool's
cleanup once it is older than the fixed 60-minute max connection age
(`openAIWSConnMaxAge`), or earlier by idle trimming (`max_idle_per_account`),
a failed health ping, or the upstream closing it. A connection that is leased
or pinned by a store-disabled continuation is not aged out while held, so a
long continuation keeps its original transport until the session ends.

Lookup failure: if reading the current account state fails (cache and
repository fallback both unavailable), that dial uses the value from the
session's account snapshot, so the toggle may lag for that dial (in either
direction); a rate-limited WARN `codex_tls_policy_lookup_failed_using_snapshot`
is logged. Failing the dial instead would break new WebSocket dials of every
Codex account during a transient cache/DB problem. No lookup is made when the
global switch is off or for accounts that can never use the policy
(non-OAuth, native relay).

## Baseline details

All baselines: null compression only, no GREASE, no ALPN (HTTP/1.1), no
padding, no ECH, no PSK. The rustls and OpenSSL baselines: record version
`0x0301`, legacy version `0x0303`, 32-byte session id, `supported_versions` =
TLS 1.3, TLS 1.2, `psk_key_exchange_modes` = psk_dhe_ke, `key_share` =
X25519MLKEM768 (1216 bytes) + X25519 (32 bytes) except the ring baseline
(X25519 only). The macOS and Windows HTTP baselines are TLS 1.2-only (below).

### `codex-0.140.0-ws-rustls-ring` (238-byte ClientHello)

- Same cipher suites (`1302 1301 1303 c02c c02b cca9 c030 c02f cca8 00ff`),
  extension set and seeded order as the 0.163.0 WS baseline.
- `supported_groups(001d 0017 0018)`, `key_share` = X25519 only (32 bytes),
  `signature_algorithms(0503 0403 0807 0806 0805 0804 0601 0501 0401)` — 9
  schemes, no `0603`, no ML-DSA.
- JA4 `t13d101000_61a7ad8aa9b6_3fcd1a44f3e3`; JA3n `f81ecb4047f8a1cd5bad262f3c97a040`.
- 35 samples / 35 distinct orders (0.116.0, 0.117.0, 0.125.0, 0.136.0–0.140.0
  Linux; 0.137.0 Windows, default CA), every order reproduced by the rustls
  seed rule.

### `codex-0.155.1-ws-rustls-awslc-pq` (1462-byte ClientHello)

- `supported_groups(11ec 001d 0017 0018)`, X25519MLKEM768 + X25519 shares with
  the X25519 key reused from the hybrid share (as captured),
  `signature_algorithms(0503 0403 0603 0807 0806 0805 0804 0601 0501 0401)` —
  10 schemes, no ML-DSA.
- JA4 `t13d101000_61a7ad8aa9b6_f9531d972513`; JA3n `54294ea073f85fb8975a09107e5dd489`.
- 30 samples / 30 distinct orders (0.141.0, 0.142.0–0.142.2, 0.147.0, 0.155.1).

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

### `codex-0.142.1-linux-http-openssl35` (1544-byte ClientHello)

Identical to `codex-0.163.0-linux-http-openssl` (below) except
`ec_point_formats` = `00 01 02` (uncompressed, ansiX962_compressed_prime,
ansiX962_compressed_char2) — OpenSSL 3.5 behaviour, hence two more bytes.
JA4 is the same (`t13d301100_1d37bd780c83_8e6e362c5eac`, it does not hash
point formats); JA3n `f6f8926ab64b2b0635c54840de2c82f9` vs
`97da657c26297bdaa441f1f09c61a2ca`. 582 samples from 0.80.0 – 0.142.1, one
structure.

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

### `codex-0.163.0-macos-http-securetransport` (171-byte ClientHello)

- TLS 1.2 only: record version `0x0301`, legacy version `0x0303`, **empty**
  session id, no `supported_versions`, no `key_share`, no
  `psk_key_exchange_modes`, no session ticket.
- Cipher suites (22): `00ff c02c c02b c024 c023 c00a c009 c008 c030 c02f c028
  c027 c014 c013 c012 009d 009c 003d 003c 0035 002f 000a` (renegotiation SCSV
  first, 3DES suites included, no `ff01` extension).
- Fixed extension order: `server_name, supported_groups(0017 0018 0019),
  ec_point_formats(00), signature_algorithms(0401 0201 0501 0601 0403 0203
  0503 0603), status_request(ocsp), signed_certificate_timestamp(18, empty),
  extended_master_secret`.
- JA4 `t12d220700_0d4ca5d4ec72_3304d8368043`; JA3n `03511c5f4b9f0d162909cf75a37e0a28`.
- 324 samples (0.163.0, 0.164.0 npm; Codex Desktop bundled CLI
  0.163.0-alpha.11.1), one structure.

### `codex-0.163.0-windows-http-schannel` (167-byte ClientHello)

- TLS 1.2 only: record version **`0x0303`** (Go writes `0x0301` for the
  first record; the dialer rewrites the ClientHello record header, which is
  not part of the handshake transcript, and refuses to send if the first
  write is not a ClientHello record), legacy version `0x0303`, empty session
  id, no `supported_versions` / `key_share` / PSK modes.
- Cipher suites (18): `c02c c02b c030 c02f c024 c023 c028 c027 c00a c009 c014
  c013 009d 009c 003d 003c 0035 002f` (no SCSV).
- Fixed extension order: `server_name, supported_groups(001d 0017 0018),
  ec_point_formats(00), signature_algorithms(0804 0805 0806 0401 0501 0201
  0403 0503 0203 0202 0601 0603), session_ticket(empty),
  extended_master_secret, renegotiation_info(00)`.
- JA4 `t12d180700_4b22cbed5bed_2dae41c691ec`; JA3n `27822e2c6c370528f63663bedad4142a`.
- 578 samples (0.137.0, 0.163.0, 0.164.0 npm; `%LOCALAPPDATA%\Programs\OpenAI\Codex`
  0.163.0; MSIX Desktop 0.163.0-alpha.10.2 and alpha.11.1), one structure.

Both TLS 1.2-only baselines set the uTLS version range to 1.2–1.2, so a
TLS 1.3-capable server's downgrade sentinel is accepted (as the native
stacks do) and a TLS 1.3-only server fails the dial. Tested handshakes:
ECDHE-ECDSA and ECDHE-RSA with AES-128/256-GCM against TLS 1.2-only Go
servers, and TLS 1.3-capable Go servers. Advertised-but-unimplemented
selections (3DES `c008`, CBC-SHA384 `c024`, DHE) fail loudly with no second
ClientHello.

Fixtures: `backend/internal/pkg/tlsfingerprint/testdata/codex-*.{json,clienthello.bin}`
(the five new fixtures are one representative raw hello per baseline plus all
captured rustls orders, generated from `/workspace/tmp/codex-tls/{capv,capr}`).
Tests compare every wire byte except random, session id and key material
against the raw captures (WS with the capture's seed for an exact order match),
reproduce every captured rustls order per generation, and complete local
handshakes for every baseline.

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
  reqwest to rustls with `h2,http/1.1` ALPN).

### Version matrix captures and boundaries

Official npm `@openai/codex` builds (Linux musl x86_64) 0.80.0 – 0.164.0 were
captured with the same loopback harness, default CA (`capv/`, sanitized
summaries in `out/SUMMARY.tsv`, `out/<platform>-<version>-<role>.json`;
binaries and npm publish times in `out/ARTIFACTS.tsv`). macOS (`capr/mac`) and
Windows (`capr/win`) were captured on real hosts. Each boundary lies between
two **adjacent stable releases**, both captured:

| Boundary | Last old / first new (npm publish, UTC) | What changed | Evidence |
| --- | --- | --- | --- |
| WS appears | 0.115.0 (2026-03-16) / 0.116.0 (2026-03-19) | default config starts dialing the Responses WebSocket | 0.115.0 and older: no WS connection in any run |
| WS ring → aws-lc-rs PQ | 0.140.0 (2026-06-15) / 0.141.0 (2026-06-18) | rustls crypto provider for the WS client (no Cargo.lock version change: rustls 0.23.36, aws-lc-rs 1.16.2 were already locked) | captures |
| WS + ML-DSA | 0.155.1 (2026-09-18) / 0.156.0 (2026-09-22) | Cargo.lock rustls 0.23.36 → 0.23.45, aws-lc-rs 1.16.2 → 1.18.1 | captures + lock |
| Linux HTTP OpenSSL 3.5 → 3.6 | 0.142.1 (2026-06-25 00:38) / 0.142.2 (2026-06-25 07:34) | Cargo.lock `openssl-src` 300.5.5+3.5.5 → 300.6.1+3.6.3 | captures + lock |

Prereleases select as their base version (`0.156.0-alpha.N` → the ≥ 0.156.0
baselines). An alpha cut before a boundary bump would get the newer
baseline; none was captured.

### Coverage limits

- **macOS / Windows native TLS is OS-version dependent.** The macOS hello was
  captured on macOS 15.3.1 (arm64) only, the Windows hello on Windows 11
  build 26200 (x86_64) only. Other OS releases (older Windows 10 builds,
  macOS 13/14/26) may send different suites/extensions; they get these
  baselines anyway. The native stacks rejected the local CA (they ignore
  `SSL_CERT_DIR`), so the hello was observed on every `chatgpt.com`
  native-tls connection but no HTTP request was decrypted; macOS was only
  captured for 0.163.x/0.164.0 and Windows for 0.137.0 and 0.163.x/0.164.0
  (identical); other Codex versions on these OSes are assumed identical
  (native-tls has used Security.framework / SChannel throughout).
- **macOS/Windows WebSocket at 0.16x** was captured with
  `CODEX_CA_CERTIFICATE` set (the only way to get the client to trust the
  local CA). This is equivalent to the default: the WS client is rustls in
  both CA modes (shown on Linux, where custom-CA and default-CA WS hellos are
  byte-identical, and on Windows 0.137.0, whose default-CA and custom-CA WS
  hellos are identical too); the CA setting only changes certificate
  verification. All mac/win WS hellos are byte-identical to the Linux ones.
- **Codex Desktop (Electron) own traffic** — e.g. `Codex Desktop/26.x
  (Mac OS ...; arm64)` from the Chromium network stack — is a different TLS
  stack and is not covered. Those requests go through ChatGPT/Desktop paths
  that never get a Codex policy (standard TLS). Only the Desktop-bundled
  `codex` CLI (`Codex Desktop/0.16x...` UAs, whose hellos match the CLI) is
  covered.

## Residual differences (known, accepted for v1)

1. **Advertised but not negotiable algorithms.** uTLS can serialize ML-DSA and
   ed448/brainpool/DSA/SHA-224 signature schemes, x448/ffdhe groups, DHE and
   CBC/ETM suites, but cannot use them. A server that selects one fails the
   handshake loudly (tested: HRR to x448/ffdhe2048, TLS 1.2 DHE suite); there
   is no fallback or downgrade. This is a compatibility limitation: it is not
   known locally which algorithms the production endpoint might ever select;
   the local captures only show TLS 1.3 with X25519MLKEM768. HRR to P-256/P-384
   works.
2. **HRR cookie position.** On a HelloRetryRequest with a cookie, uTLS inserts
   the `cookie` extension at a random position; rustls places it by the same
   seeded hash. The rest of the retried hello keeps the original order
   (tested). Go/Cloudflare-style HRRs without cookies are unaffected.
3. **No resumption.** Official rustls resumes later connections with PSK, and
   the simulation always sends the first-connection shape. (OpenSSL/native-tls
   did not resume in any capture.)
4. **Post-handshake behavior** (record sizes, CCS timing, alerts, HTTP/1.1
   header order) is Go's, not rustls/OpenSSL's. Headers are a separate concern.
5. **Platforms/environments**: Linux (all versions from 0.80.0), macOS 15.3.1
   and Windows build 26200 native stacks, default CA. Custom-CA shapes are out
   of scope; see "Coverage limits".
6. Requests listed as "not covered" above still use standard Go TLS.
7. **UA/TLS consistency only as good as the UA.** A client whose UA does not
   parse gets the newest Linux baselines, even if it is really Windows/macOS.

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
pinned one), add fixtures and tests, add a rule to `codexBaselineRules`
(`codex_selection.go`) with the first affected version as lower bound and,
if it is the newest, switch `CodexHTTPTransportPolicy` /
`CodexWSTransportPolicy` (the unparsable-UA default). The policy key includes
the baseline, so HTTP pools and WebSocket clients roll over automatically for
**new dials**. Already-upgraded WebSocket connections keep the baseline (and
upgrade UA) they were dialed with for their whole lifetime and are still
reused by the per-account pool until they age out or close (see "Toggle
semantics").
