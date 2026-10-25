# API Key revocation and Codex identity errors

Key authentication precedes Codex client policy checks. An invalid or deleted
Key must be rejected by authentication, before proxy policy, account selection,
HTTP model handling, or WebSocket upgrade.

## Delete and authentication cache ordering

`APIKeyService.Delete` commits the repository's soft deletion before invalidating
authentication caches. Previously, clearing caches before deletion allowed a
concurrent request to reload the active database row and retain it after deletion.
Cache invalidation now uses a short context independent of the caller's
cancellation so a disconnect after commit does not skip revocation.

Moving invalidation alone is insufficient: a database read may begin before
deletion and finish after invalidation. Cache publication is therefore fenced:

- Local striped guards track invalidation epochs. Old database and Redis reads
  cannot repopulate L1 after a local invalidation or its Pub/Sub notification.
- Redis invalidation atomically increments a hash-keyed revision and removes the
  cached entry. A database read records that revision before reading; its cache
  write succeeds only if the revision still matches. This also rejects late
  fills from another Gateway instance before it receives Pub/Sub.
- An invalidated load retries the database snapshot at most three times. This
  does not retry a model request. Invalidation forgets the old singleflight load
  so a new caller does not join it.
- An unavailable Redis fence leaves the fresh database result uncached. Existing
  in-memory cache invalidation still uses Pub/Sub and TTL; this does not add a
  Redis round trip to every authenticated L1 hit.

Revision entries store only a digest and counter and have no expiry, preventing
an indefinitely delayed reader from seeing a reset revision and publishing an
obsolete snapshot. This adds one small Redis counter per invalidated Key, with
no database migration. Existing admitted requests and established WebSocket
sessions are not forcibly terminated by Key deletion. Cross-instance warm L1
revocation still depends on notification delivery or its existing TTL.

## Client-facing errors

For `/v1/responses`, `/responses`, and their subpaths, unknown/deleted Keys
return HTTP **401**, preserving top-level `INVALID_API_KEY` and adding the
OpenAI `error.code`, `error.type`, and `error.message` envelope. The user-facing
message is:

> API key 无效或已删除，请更新 SAIAI Key 后重新初始化 Codex。

The error deliberately does not distinguish an unknown Key from a deleted one.
Other protocols retain their existing error shapes.

An authenticated official Codex request that fails a group's `local_proxy_only`
shape check keeps the existing 403 and error type but now explains that local
proxy identity information is incomplete. The HTTP response and WebSocket close
reason both direct the user to initialize again and restart the proxy and Codex.
Unsupported client families receive the separate official-client/proxy policy
message. Admission rules are unchanged; model discovery still does not require
the optional account/version headers.

Policy rejection logs include `missing_version` and `missing_account_id` as
booleans. They contain no raw account ID, authorization value, cookies, or body.

## Codex China-region timezone guard

Groups have an opt-in boolean `codex_cn_timezone_guard` (default off; migration
114) and an action `codex_timezone_action` (`reject` | `rewrite`, default
`reject`; migration 115). The guard covers Responses HTTP (`/v1/responses`,
`/responses` and their subpaths such as `/responses/compact`) and Responses
WebSocket requests. Effective behavior:

| guard | action  | behavior |
|-------|---------|----------|
| off   | any     | nothing (unchanged) |
| on    | reject  | reject CN/HK/Macau zones (below) |
| on    | rewrite | rewrite the environment context; never reject for the zone |

Rejections are HTTP **400** `invalid_request_error` (they were 403 in batch
114). Codex maps 403 to a retryable error (WebSocket retries, HTTPS fallback,
more retries) but 400 to a non-retryable invalid request, so a rejected user
sees the message once.

### Reject

A request is rejected when any decoded JSON string of the request (history
included) contains `<timezone>ZONE</timezone>` with a trimmed, case-sensitive
ZONE in `Asia/Shanghai`, `Asia/Urumqi`, `Asia/Chongqing`, `Asia/Chungking`,
`Asia/Harbin`, `Asia/Kashgar`, `PRC`, `Asia/Hong_Kong`, `Hongkong`,
`Asia/Macau` or `Asia/Macao`. JSON-escaped forms (`\u003c`, `\/`) match too.
The scan is read-only; forwarded bytes are unchanged.

### Rewrite

Codex renders each environment context as its own content item whose text is
exactly `<environment_context>`BODY`</environment_context>` (BODY XML-escapes
values; verified in codex-rs 0.163 `ContextualUserFragment::render` and in
0.163/0.164 monitor captures, including WebSocket frames). A JSON string
**value** (any depth, any role; object keys are ignored) is an environment
block only if its decoded text, trimmed of ASCII whitespace (SP, HT, LF, CR, FF, VT), starts with
`<environment_context>` and ends with `</environment_context>`. Any other
string — e.g. a user prompt that mentions the tag mid-sentence, closed or not —
is user content: never rewritten and not rejected by the block rules. As a
leak safety net, the bytes that will actually be sent are then scanned exactly
like reject mode: a CN/HK/Macau `<timezone>` value still present anywhere
(e.g. an environment block in a shape the rewrite does not recognise, or a
user who pastes such a tag) is rejected like reject mode (400
`codex_timezone_reinit_required`, log reason `cn_timezone_after_rewrite`).
Merely mentioning `<environment_context>` still passes. Inside a block string each
`<timezone>` value other than `America/Los_Angeles` becomes
`America/Los_Angeles` and each `<current_date>` value becomes a Los Angeles
date: the date of the enclosing input item's
`internal_chat_message_metadata_passthrough.create_time` (Codex 0.164+, HTTP
and WebSocket; a JSON number of epoch seconds, usually fractional) when it is
within [2020-01-01, frozen now + 24h], otherwise the server's frozen current
date. The item is the element of the top-level `input` array that contains
the block, and the key must be the item's own (before or after `content`;
same-name keys nested elsewhere are ignored); blocks outside input items, a
non-object metadata value, or a missing, non-numeric or out-of-range
`create_time` (e.g. milliseconds) use the frozen date, and a duplicated
metadata key fails closed. Strings inside the metadata, `create_time`
included, are never modified, so history blocks keep the day their item was
created (malformed blocks in metadata strings still fail closed). `<current_date status="unavailable" />` and every tag outside a block are untouched (and never cause a rejection). The clock is
frozen once per HTTP request and once per WebSocket client turn; smart-switch
re-application, retries and failover replays of that request/turn reuse it.
The zone comes from the system or the embedded tzdata; if it cannot be loaded
the request is rejected rather than falling back to UTC.

The edit is a raw JSON span replacement: strings are decoded once with a
decoded→raw offset map, only the bytes of the replaced values change and the
replacement is plain ASCII; escapes, key order and whitespace elsewhere stay
byte-identical, and rewriting twice gives the same bytes. Escaped strings
are decoded as a stream; only block content (at most 64 KiB per block, larger
blocks fail closed) is buffered, so extra memory does not grow with the
string length. Inside a block every
`<timezone…`/`<current_date…` tag must be an exact tag, or a value-less
self-closing tag with quoted attributes (`<current_date status="unavailable" />`);
tag values may not contain markup. A body without
`environment_context` and without a `\u` escape is not parsed. A block string
containing a second open or close tag (nested or multiple blocks), an unpaired or nested `<timezone>`/`<current_date>` tag inside a
block, or invalid JSON fails closed: the request is rejected (same 400 /
reinit message) and the original bytes are never forwarded.

- HTTP: applied in the handler after the client-policy check and again after
  every smart-switch advance, always from the client's original decoded body
  against the group that serves the request (rewrite group A → reject group B
  rejects; → guard-off group B sends the original). The forwarded body, the
  parse cache, the ops request context and the usage payload hash use the
  rewritten body; the session hash only reads headers/`prompt_cache_key`. When
  a zstd body is forwarded encoded (official Codex on OAuth / native relay),
  the rewritten body is re-encoded with a Codex-shaped frame header (`28 B5 2F
  FD 00 58`: no single-segment flag, checksum or content size; 2 MiB window);
  the compressed blocks are not byte-identical to libzstd. An encode failure
  rejects with 400. This is the one explicit exception to native byte
  passthrough: the native body digest is still taken at `Forward` entry and now
  protects the rewritten bytes.
- WebSocket: frames are rewritten where they are written upstream — the
  native passthrough relay (first frame, later frames, and an early-429
  replay, which is the next attempt's first frame), the API-key pool path
  (every possibly rebuilt turn, including retries) and in-connection failover
  replays — under the session's current group (re-read per turn as for the
  reject mode). Retained frames (replay tracker, continuation state) stay as
  the client sent them because every send applies the same deterministic
  rewrite; the relay's per-turn request payload hash describes the sent
  bytes, and the pool path reports the sent bytes' hash only for a turn that
  was actually rewritten (otherwise its usage hash is unchanged). The pool path's strict-state (which decides whether
  a turn keeps `previous_response_id` by comparing non-input fields of
  consecutive client frames) deliberately compares the client frames on both
  sides; the sent bytes are still rewritten.
  The reject-mode checkpoints still run and, in rewrite mode, only reject a
  payload that cannot be rewritten (the first frame is checked before account
  selection).
- Logs record `openai.codex_timezone_rewritten` (INFO: group, user, transport,
  turn, block / timezone / date counts) — never the body.

### Rejection responses

- HTTP: 400 with `error.type` `invalid_request_error` and `error.code`
  `codex_timezone_reinit_required`, before account selection. The group that
  actually serves the request is checked, including a group reached by smart
  switching; a rejection never advances smart switching and is not an account
  failure. If user-slot wait heartbeats already started the SSE stream (smart
  switch after queueing), the rejection is delivered through the existing
  stream error path (an SSE `error` event) because a status can no longer be
  sent.
- WebSocket: the first frame (and a continuation payload rebuilt with replayed
  history) is checked before account selection; each later `response.create`
  is checked against the group's current setting, read through the auth cache;
  an in-connection quota failover replay re-reads the Key (auth cache) and is
  checked against the current group setting, and the group-change check below,
  before it is resent; on
  the API-key account pool path, the exact payload of every turn (which may be
  rebuilt with replayed history) is checked right before it is written
  upstream. A rejection sends `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"codex_timezone_reinit_required","message":"..."}}`
  and then closes with 1008 `codex timezone reinit required`. Both are best
  effort: a failed frame write is logged (WARN, no payload). Turns already
  forwarded keep draining for usage.
- If the Key is rebound to another group while a WebSocket session is open,
  the next turn (or failover replay) ends the session with the same frame/close mechanism
  (`status` 409, `error.code` `api_key_group_changed`, message
  `API Key 分组已变更，请重新连接。`, close reason `api key group changed`);
  the client reconnects under the new group (409 stays retryable on purpose).
- The message tells users on a recognised Codex backend older than 0.163
  (`codex-tui`, `codex_exec`, `codex_vscode`, `codex_cli_rs`, `Codex Desktop`
  User-Agent) to upgrade; every other client gets the generic text asking for
  `saiai cli` re-initialization, a Codex restart and a new session.
- Logs record `openai.codex_timezone_guard_rejected` with group, user, reason,
  zone, UA product/version, transport and turn — never the body.

Limitations:

- Native `/codex/images` and ChatGPT conversation routes are not covered. In
  reject mode a user message that literally contains such a tag is also
  rejected (accepted false positive); rewrite mode only rewrites strings that
  are entirely one `<environment_context>` block, and its leak safety net
  rejects any CN `<timezone>` value left elsewhere, as reject mode would.
- Reject mode scans JSON object keys as well as values.
- A large JSON string that contains an escape and the word `timezone` (reject)
  or `environment_context` (rewrite) is decoded into a transient buffer up to
  its own size (rewrite also keeps an offset map for it).
- On the official-Codex OAuth relay both the error frame and the following
  1008 close are best effort; stopping the relay's client reader may close the
  socket before the close frame is sent.

## Codex version gate

Global settings `codex_version_gate_enabled` (default false),
`codex_min_version` and `codex_max_version` (SemVer, empty = unbounded;
validated on save, min ≤ max) admit official Codex clients only inside the
range. Identification is the Codex client family used for admission
(`IsCodexOfficialClientByHeaders`); other clients are unaffected. The engine
version must be stated identically by the `version` header and the
User-Agent main version (`product/VERSION …`) and parse as SemVer (a
prerelease sorts before its release); anything else is "unknown" and rejected
while the gate is on — this includes custom provider configurations that
override the `version` header. With both bounds empty every known version is
admitted and unknown versions are still rejected.

- Checked for Responses HTTP (all subpaths, including `/responses/compact`)
  before account selection / stream start (HTTP 400). WebSocket connections
  are upgraded and, once the first client frame has been read (or failed to be read), receive
  `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"codex_version_unsupported","message":"..."}}`
  and close 1008 `codex version unsupported`, before any account selection
  or upstream connection. A non-101 handshake response would be turned into
  a retryable 502 by the SAIAI client local proxy (Codex then retries the
  WebSocket and falls back to HTTPS); the error frame passes through it.
  WebSocket sessions are not re-checked: the version is fixed per connection.
- Rejection: 400 `invalid_request_error`, `error.code`
  `codex_version_unsupported`, a short message (Codex prints the whole body):
  `当前 Codex 引擎版本 X 不在允许范围 [min, max]。请安装 Y：npm i -g @openai/codex@Y；若使用后台进程再执行 codex app-server daemon update --from-cli -y，然后重启 Codex。Desktop/VS Code 请等待管理员放行。`
  where Y is the minimum when below it and the maximum when above it.
- The client is identified from headers first; other clients never read the
  settings. Settings are cached (60 s) and published as one value; an update
  is applied at once to new requests and connections. An expired value is
  served while one background refresh runs, so a slow settings store never
  blocks requests; only the first read in a process blocks (at most 1 s). A
  read failure keeps the last known good value; with none the gate fails
  open and logs an ERROR.
- Logs record `openai.codex_version_gate_rejected` (WARN: product, version,
  min, max, transport).

## Regression evidence

Service tests cover refill during deletion, late database snapshots on one or
two instances, singleflight enabled/disabled, subscriber invalidation, Redis
fence failure, and caller cancellation. The Redis integration test runs the real
Lua fence and confirms old revisions cannot restore a revoked snapshot.

Middleware tests exercise invalid Keys with missing proxy headers on HTTP,
compact aliases, and WebSocket upgrade requests. Handler tests exercise valid
Keys with each missing identity header on HTTP and WebSocket, verify rejection
before scheduling, and check sanitized diagnostic flags. Linux official Codex
0.159.2 and 0.162.1 loopback mock probes confirm the 401 message survives both
ordinary turns and final app-server remote-compaction errors. No live provider
requests or credentials are used; rendered Windows/macOS/Desktop UI behavior
is not asserted by these probes.

Targeted concurrency tests passed 20 consecutive runs, along with middleware
regressions, handler admission tests, real Redis integration, and ordinary vet
on the affected packages. Unit-tag vet reports existing unkeyed literals in
`openai_chatgpt_delivery_owner_test.go` and `openai_chatgpt_turn_cache_test.go`;
those files are unchanged. The local environment has CGO disabled and no C
compiler, so race-detector instrumentation was unavailable.
