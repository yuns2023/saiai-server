# SAIAI client proof v1 — wire contract (Server ↔ local proxy)

Goal: let the Gateway tell requests sent by the SAIAI local proxy from requests sent directly
with the API key. Both repos are public, so this is device binding + server-side visibility,
not secrecy. v1 is OBSERVE ONLY: the Gateway never rejects a request because of the proof.

## 1. Device key
- Client generates an ECDSA P-256 key pair on first use (Rust `p256` crate, enable `ecdsa`
  feature; Go `crypto/ecdsa`). Private key stored in `~/.saiai/client-device-key` (PKCS#8 PEM,
  0600 on unix; on Windows rely on the user-profile ACL), plus `~/.saiai/client-device.json`
  `{ "device_id": "...", "gateway": "<base url>", "public_key_sha256": "<hex>" }`.
- Public key encoding on the wire: base64url (no padding) of the SEC1 **uncompressed** point
  (65 bytes, leading 0x04). Server rejects anything else.

## 2. Enrollment
`POST /api/v1/client/devices/enroll` with `Authorization: Bearer <api key>` (same auth as
`/api/v1/client/bootstrap`). JSON body:
```json
{ "public_key": "<b64url 65 bytes>", "client_version": "1.1.54", "os": "windows", "arch": "x86_64" }
```
- Idempotent on (user_id, sha256(public_key)): returns the existing device.
- 200 → `{ "device_id": "cd_<22 chars b64url of 16 random bytes>", "status": "active" }`
- 403 `{ "code": "client_device_revoked" }` when that key was revoked → client stops signing for
  this gateway (does not auto-generate a new key).
- 429 `{ "code": "client_device_limit" }` when the user already has 50 non-revoked devices.
- Field limits: client_version ≤ 64, os/arch ≤ 32 chars; bigger body (>4 KiB) → 400.
- Client: enroll once per process start in the background (non-blocking), retry with backoff
  (1m, 5m, 30m cap) on network/5xx; until a device_id is known, forward requests unsigned.
  Enrollment failure must never break forwarding.

## 3. Request proof header
`X-Saiai-Client-Proof: v1.<device_id>.<ts>.<nonce>.<sig>`
- ts: unix seconds (decimal). nonce: base64url(16 random bytes) = 22 chars. Fresh per request
  attempt — every retry / reconnect / WS handshake is signed anew.
- sig: base64url of the 64-byte raw `r||s` ECDSA P-256 signature over SHA-256 of the message:
```
saiai-client-proof-v1\n
<METHOD uppercase>\n
<request target exactly as sent on the wire: path + optional "?query", no scheme/host>\n
<ts>\n
<nonce>\n
<lowercase hex sha256 of the exact API key string sent in Authorization Bearer>
```
  (lines joined with "\n", no trailing newline).
- Server takes the target from `c.Request.RequestURI` (raw, before any endpointNorm rewrite).
- Header total length cap 256; any parse error → `invalid`.
- Applies to every request the local proxy sends to the Gateway: HTTP forwarding paths and the
  WebSocket upgrade GET. WS: only the handshake is signed.
- The local proxy must never forward a client-supplied `X-Saiai-Client-Proof` (drop it, then add
  its own). The Gateway must strip it before any upstream request (all platforms/paths).

## 4. Server verification (observe mode)
Status enum (stored as smallint in `usage_logs.client_attestation`, NULL = not collected):
1 valid, 2 missing, 3 invalid (format/signature/key mismatch), 4 stale (|now-ts| > 300s),
5 replay (nonce seen; Redis `SET NX` TTL 900s), 6 unknown_device (no such device, or device
belongs to another user than the API key's user), 7 revoked, 8 unverified (Redis/DB error —
fail open, short timeout).
- Computed only when the request's group has `client_attestation_policy = observe`; `off` →
  NULL. Never rejects in v1 (`enforce` reserved, not selectable yet).
- Device lookups cached in-process ≤ 60s; revocation invalidates the local cache entry
  immediately (other instances converge within the TTL).
- On `valid`: update device `last_seen_at`, `last_ip`, and `last_claude_at` or `last_codex_at`
  (by platform of the route/group), throttled per (device, field) to once per 5 min.

## 5. Admin
- Users list: column "Codex 设备" = number of non-revoked devices with `last_codex_at` within 30
  days (clicking opens the user's client-device modal, with revoke); column "客户端校验（7天）"
  aggregating `usage_logs.client_attestation` for the current page's user ids over 7 days
  (billing requests only — say so in the header tooltip).
- Usage table: badge on rows whose status is not valid/NULL.
- Group edit: `client_attestation_policy` select (off / observe).
- Neutral wording everywhere (no "逆向/作弊"). Labels: 通过 / 未携带 / 校验失败 / 时间偏差 /
  重复请求 / 设备未知 / 设备已吊销 / 未校验 / 未采集.

## 6. Server implementation notes (v1, observe)

These notes describe how the Gateway implements the contract above; they do not
change the wire format.

- Test vector: `backend/internal/pkg/clientproof/testdata/client-proof-v1-vector.json`
  (generated independently with Python `cryptography`). The pure verifier in
  `backend/internal/pkg/clientproof` must accept it and reject a 1-byte target
  change, another API key and a timestamp outside ±300 s.
- Schema: migration `116_client_devices_and_attestation.sql` adds
  `client_devices`, nullable `usage_logs.client_attestation SMALLINT` (no
  default, no backfill, no index) and `groups.client_attestation_policy`
  (`off` default; CHECK allows `off|observe|enforce`; the admin API accepts only
  `off|observe`). The 7-day admin aggregate uses the existing
  `idx_usage_logs_user_created (user_id, created_at)`.
- Enrollment responses are the flat JSON bodies of §2 (not the admin
  `{code,message,data}` envelope). Validation failures return
  `400 {"code":"invalid_request","message":...}`. Enrollment requires normal
  API-key authentication; the device belongs to the key's user
  (`first_api_key_id` is informational).
- Middleware placement: `middleware.ClientProof` runs immediately after API-key
  authentication on every authenticated gateway route (`/v1/*`, root
  `/responses*`, `/backend-api/codex/models`, `/chatgpt/backend-api/*`,
  `/chatgpt/api/estuary/upload_content_bytes`, `/v1beta/*`). WebSocket upgrades
  (`GET /v1/responses`, `GET /responses`) pass through it; the handshake status
  is recorded on every turn of that connection.
- Target: `c.Request.RequestURI`, the raw request-line target set by
  `net/http`. No Gateway middleware rewrites it (`InboundEndpointMiddleware` only
  stores a normalized copy in the context). A reverse proxy in front of the
  Gateway must forward the request target unchanged (the shipped Caddyfile's
  plain `reverse_proxy` does); a proxy that normalizes or re-encodes paths makes
  proofs `invalid`.
- Signed API key: the `Authorization: Bearer` value exactly as API-key
  authentication parses it (scheme case-insensitive, value trimmed). API-key
  authentication looks keys up verbatim (no OAuth-style mapping), so on the
  Claude path this is Claude Code's own Bearer value forwarded by the local
  proxy. `x-api-key` / `x-goog-api-key` are never the hash input: without a
  Bearer token a request carrying no proof is `missing`, and any proof sent
  without a Bearer token is `invalid` (including one signed over `sha256("")`).
- Verification order: missing → parse (`invalid`) → device lookup (`unverified`
  on DB error/timeout; `unknown_device` when absent or owned by another user) →
  signature (`invalid`) → revoked → freshness (`stale`) → nonce `SET NX EX 900`
  (`replay`; `unverified` on Redis error/timeout) → `valid`. Only otherwise-valid
  proofs consume a nonce. More than one proof header is `invalid`. The shared
  Redis client ignores context deadlines on socket I/O, so the nonce store
  waits for the command only until the 200 ms deadline; at most 64 commands run
  in the background (more fail fast as `unverified`), each bounded by a 2 s
  pool/retry timeout plus the client `ReadTimeout`. If the verifier is not configured, observe groups record
  `unverified`.
- Policy: computed when the authenticated key's group has `observe` (or the
  reserved `enforce`, treated the same in v1), or when the key has smart
  switching enabled (a later switch may land in an observe group; such a
  precompute in an off group never updates device `last_*`); usage rows
  store the status only when the final (post smart-switch) key's group
  collects attestation, otherwise NULL. Requests are never rejected.
- Caching: device lookups are cached in-process for 60 s (absent devices for
  10 s); revoking through the admin API drops the local entry immediately, and a
  lookup that raced a local revoke does not repopulate the cache.
  `last_seen_at`/`last_ip` and `last_claude_at`/`last_codex_at` (by group
  platform: OpenAI → Codex, Anthropic → Claude) are updated only for `valid`
  proofs, at most once per (device, field) per 5 minutes per instance, off the
  request path.
- Header stripping: the middleware deletes `X-Saiai-Client-Proof` from the
  inbound request on every gateway route regardless of policy, and the OpenAI
  header denylist (HTTP, WebSocket, native relay, images, Chat updates) also
  refuses it. Claude OAuth uses an allowlist and Claude API-key passthrough drops
  all `x-saiai-*` headers.
- Admin: `GET/DELETE /api/v1/admin/users/:id/client-devices[/:device_id]`
  (row id). The user list adds `codex_device_count` and
  `client_attestation_7d` (status name → count, NULL rows excluded); either is
  `null` when its aggregate fails. Admin usage rows expose `client_attestation`.
