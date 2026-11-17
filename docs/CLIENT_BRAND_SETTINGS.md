# Per-site client brand

One Gateway binary serves several sites, and each site has its own settings
table. A few client-facing error texts name SAIAI or tell the user to re-run
the SAIAI client setup. A site that ships another client (ccyou.dev uses AITP,
re-initialized with `aitp init`) sets two admin-only settings to reword them:

| Setting | Meaning | Limit |
|---|---|---|
| `client_brand_name` | Replaces SAIAI in the texts below | 32 characters |
| `client_setup_command` | The command named where a text says to re-run the client setup | 64 characters |

Example values for ccyou.dev: brand `CCYou.dev`, command `aitp init`.

## Semantics

| `client_brand_name` | `client_setup_command` | Result |
|---|---|---|
| empty | empty | Every text is byte-identical to the original SAIAI constant (default) |
| set | empty | The brand replaces SAIAI; the setup step is worded neutrally |
| empty | set | The setup step names the command; the brand is dropped or replaced by a neutral word |
| set | set | Both |

- Empty means absent, empty or whitespace only. Leaving both empty is the only
  way to keep the SAIAI wording.
- Once either is set, none of the texts below names SAIAI. Each text keeps its
  language: English texts say ``run `aitp init` ``, Chinese texts say
  `重新运行 aitp init`.
- HTTP status, error `type`/`code` and every other response field stay the
  same. Only the message (or WebSocket close reason) changes.
- There is no fallback to `site_name`.

## Validation

The admin API validates a field only when the request contains it:

- Surrounding white space is trimmed. Length is counted in characters
  (Unicode code points) after trimming.
- The value must be valid UTF-8, and every character must be printable
  (Go `unicode.IsPrint`). Control characters, line breaks, tabs, zero-width
  characters and spaces other than the ASCII space are rejected.
- The value must not contain the restricted upstream identity that the
  client-safe scrubber removes, and must not contain `saiai` in any letter
  case.
- A rejected value returns 400 `INVALID_CLIENT_BRAND_NAME` or
  `INVALID_CLIENT_SETUP_COMMAND`, and nothing is saved.

A stored value that fails these rules can only come from a direct database
write. It is ignored on read (a warning is logged), so the site uses the
other field alone or the original texts. It does not make a PUT that omits the
field fail.

## Admin API and UI

- `GET /api/v1/admin/settings` returns `client_brand_name` and
  `client_setup_command`. They are not public settings.
- `PUT /api/v1/admin/settings` keeps the previous value of a field that is
  absent from the request or `null`, so admin pages and scripts that predate
  the fields do not wipe them. `""` clears a field. A change is listed in the
  settings audit log line (`AUDIT: settings updated … changed=[…]`).
- Classic admin console: 系统设置 → 通用设置 → 站点设置, the two inputs
  客户端品牌名 / 客户端初始化命令 below the site name and subtitle.

## Reading: cache and per-request snapshot

- `SettingService.ClientBrand` keeps a per-instance cache with a 60 s TTL.
  After expiry the old value is still served while one background refresh
  runs (5 s timeout), so requests do not wait on the settings store. Only the
  first read in a process blocks, for at most 1 s.
- A failed refresh keeps the last value read successfully and retries after
  5 s. If no value was ever read, the original texts are used and the read is
  retried after 5 s.
- An admin save publishes the new value at once on the instance that handled
  the PUT; a load that was already in flight cannot overwrite it. Another
  instance using the same database picks the value up when its cache expires
  (about a minute).
- The global middleware `middleware.ClientBrand` (registered right after
  `MaintenanceMode` in `backend/internal/server/router.go`) reads the value
  once per request
  and puts it in the request context. An unbranded site attaches nothing; the
  zero value means the original texts.
- SSE streams and WebSocket sessions take the snapshot once, at stream or
  session start, and per-frame code never reads settings. A change therefore
  applies to new requests and new connections only; an open WebSocket session
  keeps the wording of its upgrade request.

## Converted texts

The example column uses brand `CCYou.dev` and command `aitp init`. The last
column gives the wording when only one of the two settings is set. Source
paths are relative to `backend/internal/`.

### Claude (Anthropic) routes

| Text | Original | Example | When one setting is empty |
|---|---|---|---|
| Claude Code required (EN), `claudeOAuthRealClaudeCodeRequiredMessage` | Please use the official Claude Code client. If it is already installed, run SAIAI CLI initialization and try again. Contact your administrator if the issue persists. | … If it is already installed, run `` `aitp init` `` and try again. … | No command: … If it is already installed, re-run your client setup and try again. … (the text never names the brand) |
| Service channel unavailable (ZH), `DeviceAuthorizationUnavailableClientMessage` | SAIAI 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。 | CCYou.dev 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。 | No brand: 服务通道暂时不可用，请稍后重试；如持续出现，请联系支持。 |

Where they reach the client:

- Claude Code required: the Claude OAuth request-identity gate and the pinned
  mode login-state gate (`service/gateway_service.go`) and the group
  request-shape gate (`service/carpool_billing_validate.go`) answer HTTP 400
  `invalid_request_error`, as an Anthropic JSON error or an SSE error event.
  The carpool billing-integrity check in the same file puts the text in the
  body of an account failover error, which reaches the client when no other
  account takes the request.
- Service channel unavailable: `ClientSafeUpstreamErrorMessage` replaces any
  client error message that names the restricted upstream identity with this
  text. That covers the device-authorization account failure after failover
  is exhausted (HTTP 502), a raw upstream 400 naming the identity (HTTP 502),
  Anthropic JSON and SSE errors (`errorResponse`, `handleStreamingAwareError`),
  error passthrough rules, OpenAI `writeError` JSON and SSE, and the WebSocket
  billing rejection frame and close reason.

### Codex (OpenAI) routes

| Text | Original | Example | When one setting is empty |
|---|---|---|---|
| Upstream capacity (EN), code `saiai_upstream_capacity_unavailable` | SAIAI upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your SAIAI balance or quota. | CCYou.dev upstream capacity is temporarily unavailable. … This is not your CCYou.dev balance or quota. | No brand: Upstream capacity is temporarily unavailable. Please retry later or contact your administrator. This is not your account balance or quota. |
| Local proxy identity incomplete (ZH), HTTP 403 `saiai_local_proxy_required` | 本地代理身份信息不完整，请重新运行 SAIAI Codex 初始化并重启代理及 Codex。 | 本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。 | No command: 本地代理身份信息不完整，请重新运行客户端初始化并重启代理及 Codex。 |
| Local proxy required (ZH), HTTP 403 `saiai_local_proxy_required` | 此分组仅允许通过 SAIAI 本地代理使用官方 Codex 客户端。 | 此分组仅允许通过 CCYou.dev 本地代理使用官方 Codex 客户端。 | No brand: 此分组仅允许通过本地代理使用官方 Codex 客户端。 |
| WebSocket close reason 1008, local proxy required (EN) | SAIAI local proxy with official Codex required | CCYou.dev local proxy with official Codex required | No brand, or the branded reason is over 120 bytes: Local proxy with official Codex required |
| WebSocket close reason 1008, official Codex with incomplete identity (ZH, names no brand) | 本地代理身份信息不完整，请重新初始化并重启代理及 Codex。 | 本地代理身份信息不完整，请重新运行 aitp init 并重启代理及 Codex。 | No command, or the reason is over 120 bytes: unchanged |
| Timezone guard, generic (ZH), code `codex_timezone_reinit_required` | 请求内容或历史中包含中国地区时区信息。请重新运行 saiai cli 初始化（会关闭 Codex 的时区上报），重启 Codex 并新开会话后再使用。 | …请重新运行 aitp init（会关闭 Codex 的时区上报），… | No command: …请重新运行客户端初始化（会关闭 Codex 的时区上报），… |
| Timezone guard, Codex older than 0.163 (ZH) | 当前 Codex 版本过旧，会上报本机时区。请将 Codex 升级到 0.163 或以上，重新运行 saiai cli 初始化，重启 Codex 并新开会话后再使用。 | …升级到 0.163 或以上，重新运行 aitp init，重启 Codex 并新开会话后再使用。 | No command: …重新运行客户端初始化，… |
| Invalid or deleted Key on Responses routes (ZH), HTTP 401 `INVALID_API_KEY` | API key 无效或已删除，请更新 SAIAI Key 后重新初始化 Codex。 | API key 无效或已删除，请更新 CCYou.dev Key 后重新运行 aitp init。 | No brand: …请更新 API Key 后重新运行 aitp init。 No command: …请更新 CCYou.dev Key 后重新初始化 Codex。 |

Notes:

- The capacity message is used on every normalizer branch (plain error,
  `response.failed`, failed `response.done`) over HTTP (503 error body,
  passthrough-rule path, non-streaming bodies), SSE and WebSocket (forwarder
  events, ingress client writes, v2 passthrough frames). Only `message`
  changes.
- The WebSocket close reasons are cut at 120 bytes by `closeOpenAIClientWS`,
  so a longer branded reason falls back as shown. The local-proxy reason
  fits a brand of up to 79 bytes, so every ASCII brand fits; a long CJK brand
  may not. The identity reason fits a command of up to 40 bytes.
- The timezone texts are sent as HTTP 400, an SSE error event, or a WebSocket
  error frame. The Codex Desktop text names no setup step and never changes.
- Sources: `service/openai_codex_capacity_error.go`,
  `handler/openai_gateway_handler.go`, `handler/openai_codex_timezone_guard.go`,
  `server/middleware/api_key_auth.go`.

### Account and payment

| Text | Original | Example | When one setting is empty |
|---|---|---|---|
| TOTP issuer, `service/totp_service.go` | SAiAi | CCYou.dev | No brand: 2FA |
| Balance recharge order subject sent to the payment provider, `service/payment_service.go` | SAIAI Balance Recharge | CCYou.dev Balance Recharge | No brand: Balance Recharge |

The TOTP issuer applies to new enrollments only. An authenticator app keeps
the issuer it stored when the user enrolled.

## Deliberately not changed

- Error codes and types that contain `saiai`: `saiai_upstream_capacity_unavailable`
  and `saiai_local_proxy_required` are codes, not text.
- `X-SAIAI-*` and `X-Saiai-Client-Proof` headers, `SAIAI_*` environment
  variables, log lines and code comments.
- The `/saiai-cli` route and its texts, which serve the SAIAI client itself.
- The experimental native-chat updates route
  (`/chatgpt/backend-api/saiai/chat-updates`) and its `saiai_device` query.
- The `site_name` default `SAiAi`, which is already set per site.
- The setup wizard banner, server start-up logs, and the
  `saiai-cryptopay/1` RPC User-Agent.
- Text written by administrators in error passthrough rules (`CustomMessage`)
  and upstream bodies passed through to the client (passthrough bodies, raw
  upstream 400 bodies). Only the existing restricted-identity scrub applies:
  a message that names the restricted upstream identity is replaced as a
  whole by the branded service-channel text above. Administrators must keep
  SAIAI out of their own rule texts; the admin hint says so.
- Frontend copy. Only backend texts sent to clients are covered.
- Ops records. Ops-only paths (`setOpsLocalFailure`, WebSocket upstream
  failure records, Ops log filter proposals) pass the empty brand, so raw
  upstream text scrubbed for Ops uses the original wording. A message that was
  already worded for the client is recorded as sent. Nothing classifies Ops
  rows by this text.

## Tests

Paths are relative to `backend/internal/`.

- `service/client_brand_test.go` (`-tags unit`): byte-identical originals,
  brand-only / command-only / both texts, validation, cache behaviour, the
  service exit paths, and a non-failover JSON error passthrough rule.
- `handler/client_brand_messages_test.go`: Codex local-proxy, timezone and
  WebSocket texts, close-reason byte limits, Anthropic and OpenAI error
  writers, and a failover SSE error passthrough rule.
- `server/middleware/client_brand_test.go` (`-tags unit`): invalid-Key texts
  and the settings → middleware → API-key middleware path.
- `handler/admin/setting_handler_client_brand_test.go`: admin GET/PUT,
  keep-previous, validation, clearing and the audit diff.
