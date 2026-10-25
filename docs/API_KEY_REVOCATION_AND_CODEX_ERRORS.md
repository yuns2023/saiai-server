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
114). When it is on, Responses HTTP (`/v1/responses`, `/responses` and their
subpaths such as `/responses/compact`) and Responses WebSocket requests are
rejected when any decoded JSON string of the request (history included) contains
`<timezone>ZONE</timezone>` with a trimmed, case-sensitive ZONE in
`Asia/Shanghai`, `Asia/Urumqi`, `Asia/Chongqing`, `Asia/Chungking`,
`Asia/Harbin`, `Asia/Kashgar`, `PRC`, `Asia/Hong_Kong`, `Hongkong`,
`Asia/Macau` or `Asia/Macao`. JSON-escaped forms (`\u003c`, `\/`) match too.
The scan is read-only; forwarded bytes are unchanged.

- HTTP: 403 with `error.type` `permission_error` and `error.code`
  `codex_timezone_reinit_required`, before account selection. The group that
  actually serves the request is checked, including a group reached by smart
  switching; a rejection never advances smart switching and is not an account
  failure.
- WebSocket: the first frame (and a continuation payload rebuilt with replayed
  history) is checked before account selection; each later `response.create`
  is checked against the group's current setting, read through the auth cache;
  an in-connection failover replay is checked again before it is resent. A
  rejection sends `{"type":"error","status":403,"error":{"type":"permission_error","code":"codex_timezone_reinit_required","message":"..."}}`
  and then closes with 1008 `codex timezone reinit required`. Turns already
  forwarded keep draining for usage.
- The message tells users on a recognised Codex backend older than 0.163
  (`codex-tui`, `codex_exec`, `codex_vscode`, `codex_cli_rs`, `Codex Desktop`
  User-Agent) to upgrade; every other client gets the generic text asking for
  `saiai cli` re-initialization, a Codex restart and a new session.
- Logs record `openai.codex_timezone_guard_rejected` with group, user, zone,
  UA product/version, transport and turn — never the body.

Native `/codex/images` and ChatGPT conversation routes are not covered. A user
message that literally contains such a tag is also rejected (accepted false
positive).

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
