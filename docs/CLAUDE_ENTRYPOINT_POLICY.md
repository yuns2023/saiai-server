# Claude client entrypoint allowlist

Per-group filter for `POST /v1/messages` that only lets requests through when
the Claude client **declares** an allowed entrypoint. It was added for a
single-device reclaude Claude subscription account that upstream revoked
30–60 s after it served requests from T3 Code, a third-party GUI that drives
the official Claude Code through the Agent SDK. T3 Code passes the
`claude_code_only` checks (official UA prefix and system prompt), but declares
its own entrypoint:

```
User-Agent: claude-cli/2.1.316 (external, t3-code, agent-sdk/0.3.276)
system:     x-anthropic-billing-header: cc_version=2.1.316.abc; cc_entrypoint=t3-code; cch=12345;
```

The entrypoint is chosen by the client. The policy filters clients that
honestly report themselves; it is **not** proof that a request came from an
official client, and the error message never claims that it is.

## Settings (groups, migration 117)

| Column | Default | Meaning |
| --- | --- | --- |
| `claude_entrypoint_policy` | `off` | `off`, `observe` or `enforce`. Only `off` is allowed for non-anthropic groups (admin API returns 400). |
| `claude_entrypoint_allowlist` | `["cli","claude-vscode","sdk-cli"]` | Allowed entrypoints. Items are trimmed and lower-cased, must match `^[a-z0-9][a-z0-9._-]{0,63}$`, are de-duplicated, at most 32. `[]` allows nothing. A CHECK keeps it a JSON array. |

Admin API (`POST/PUT /api/v1/admin/groups`): an absent
`claude_entrypoint_allowlist` keeps the stored list on update (uses the default
on create); `[]` stores an empty list; `null` or a non-array is a 400. Both
fields are in the API key auth cache snapshot (schema v5), and every group
update invalidates the auth cache of the group's keys.

| Mode | Behavior |
| --- | --- |
| `off` | Nothing is parsed or recorded. |
| `observe` | Every request is evaluated and recorded; nothing is rejected. |
| `enforce` | Every request is evaluated and recorded; non-compliant requests get 400. |

## What is parsed (read-only, once per request)

`claudebilling.ParseEntrypointFacts` never mutates the body and never looks
outside the top-level `system` field.

- **User-Agent** (case-insensitive): `^claude-cli/(\d+\.\d+\.\d+)\S*\s+\(([^,()]+),\s*([^,()]+)(?:,[^()]*)?\)`.
  The entrypoint is the token after the user type, trimmed and lower-cased:
  `(external, cli, workload/cron)` → `cli`. The comment must be closed by `)`.
  Any other UA has no entrypoint. A request with more than one `User-Agent`
  header value has an ambiguous UA (recorded as `<ambiguous>`, reason
  `ua_ambiguous`).
- **Body**: the JSON must have exactly one top-level `system` key (two keys,
  even with an escaped name, → `ambiguous`). A string `system` is scanned; an
  array contributes the `text` of every `{"type":"text"}` element (JSON
  escapes decoded). Exactly one `x-anthropic-billing-header:` must appear
  (0 → `missing`, more → `ambiguous`); on that header's line exactly one
  non-empty `cc_entrypoint=<value>;` must appear (none/empty → `malformed`,
  several → `ambiguous`; a single unterminated one → `malformed`). Fields are
  split on `;` and only a field named
  exactly `cc_entrypoint` counts. A `system` block with duplicate `type` or
  `text` keys is `ambiguous`. A header in `messages` or `tools` is ignored.

## Decision

Compliant iff the UA entrypoint is allowed, the body status is `valid`, the
body entrypoint is allowed and both entrypoints are equal. The first failing
check gives the reason: `ua_ambiguous`, `ua_not_allowed`, `body_missing`,
`body_malformed`, `body_ambiguous`, `body_not_allowed`, `mismatch` (`ok` when
compliant).

**Aux probe exemption.** Official Claude Code sends a haiku `max_tokens=1`
probe that may have no billing header. When the UA entrypoint is allowed,
the body status is `missing` **and** the request matches the existing Claude
OAuth shape-gate exemption for that probe (`isClaudeOAuthHaikuMaxTokensOneAuxRequest`:
haiku model, `max_tokens=1`, not streaming, OAuth beta, claude-cli UA,
metadata user_id with device and session), the request is compliant with
reason `aux_body_missing_allowed` (result `allowed`). Malformed, ambiguous,
not-allowed or mismatched bodies still fail; other small requests get no
exemption.

## Where it runs

Only `GatewayHandler.Messages`. Not `count_tokens`, `/v1/models` or the
OpenAI-compatible routes. Requests with a forced platform (for example the
`/antigravity` routes) skip the policy, as `claude_code_only` does; the
prompt-too-long fallback clears the forced platform and is then checked. A request is checked against every group it can be
sent through, each with that group's own policy:

1. the key's group after auth (including an auth-time smart switch), before
   the Claude Code version check, so version-rejected clients are recorded;
2. the target group of an in-request smart switch;
3. the invalid-request fallback group (prompt too long);
4. for each of the above, the `claude_code_only` fallback group the account
   scheduler resolves to when the request is not recognized as Claude Code.

So smart switching from an `off` group A to an `enforce` group B rejects at B.
If the `claude_code_only` fallback chain cannot be resolved for the check, the
request fails closed with the same 503 account selection would return.

Known limitation: the fallback chain is resolved for the check and again by
account selection. If an admin changes the chain in between (while the
request waits for a slot), selection can use a group that was not checked.

## Rejection

```
HTTP 400
{"type":"error","error":{"type":"invalid_request_error","code":"claude_entrypoint_not_allowed",
 "message":"This group does not allow this Claude client entrypoint (UA: t3-code, body: t3-code). Allowed: cli, claude-vscode, sdk-cli."}}
```

If the SSE stream has already started (keep-alive pings while waiting for a
slot), the same JSON is sent as a `data:` event, as for other stream-aware
errors. Non-compliant requests also log WARN `gateway.claude_entrypoint_policy`
(group_id, policy, result, reason, ua/body entrypoint, body_status, user_id,
api_key_id), at most once per group + entrypoint pair per one-minute window,
at most 60 lines per window and 1000 tracked pairs per window per process;
suppressed lines are counted and summarized
(`gateway.claude_entrypoint_policy_logs_suppressed`) when the window rolls over.

Enforce-mode rejections are **not** written to `usage_logs` (nothing was
served or billed). They appear in the Ops error log as 400
`claude_entrypoint_not_allowed` and in the observation list as `rejected`.

## Per-request warning in usage records

`usage_logs.client_entrypoint` (JSONB, nullable, migration 117) is set only
for a request that an `observe` group found non-compliant but that was still
served, for example:

```json
{"ua":"t3-code","body":"t3-code","body_status":"valid","reason":"ua_not_allowed","group_id":1,"policy":"observe"}
```

Values are sanitized as in the observation list. If several groups evaluated
the request, the first non-compliant one is stored. Compliant or exempted
(`aux_body_missing_allowed`) requests, groups with policy `off` and requests
before migration 117 store NULL. It is admin-only: the admin usage list shows
an "入口告警" badge with a tooltip (UA entrypoint, body entrypoint, reason,
group, mode) and has a "仅看入口告警" filter (`entrypoint_flagged=true`,
`client_entrypoint IS NOT NULL`; charts are hidden while it is on). User-facing
usage views and APIs never include it.

## Observation list

Table `group_claude_entrypoint_observations`, one row per UTC day and
(group, UA entrypoint, body entrypoint, body status, CLI version, policy,
result, reason) with a request count, first/last seen and the latest
User-Agent sample (≤256 chars). Results: `allowed`, `violation_observed`
(observe), `rejected` (enforce). Entrypoints are stored sanitized:
`<none>` when absent, `<invalid>` when they contain characters outside
`[a-z0-9._/-]`, otherwise truncated to 64 bytes.

- Each (request, group) is counted once.
- Writes are aggregated in memory and flushed every 30 s in one upsert batch,
  and on graceful shutdown. At most 5000 distinct keys are buffered, and at
  most 1000 per group per flush window, so one group cannot starve the others;
  further new keys are dropped and counted (WARN
  `gateway.claude_entrypoint_observations_dropped`). A failed batch is logged
  and dropped, never retried. The request path never waits on the database.
- First/last seen are kept as min/max even if the clock steps back; the
  sample is replaced only by a request at or after the current last seen.
  Only the latest User-Agent sample per row is kept.
- Rows outside the last 90 UTC days (today included) are deleted once a day.
- `GET /api/v1/admin/groups/:id/claude-entrypoint-observations?days=7`
  (`days` 1..90) aggregates the range by (UA entrypoint, body entrypoint, body
  status, version, result, reason), newest first, at most 500 rows
  (`truncated: true` when more exist). It also returns
  `collector: {dropped_last_flush, last_error_at}` for this process. Rows
  still in memory (up to 30 s) are not included.

The admin group edit dialog (anthropic groups) has the mode select, the
allowlist editor and this list; "加入白名单 / Allow" adds a row's UA
entrypoint to the form's allowlist, which still has to be saved.

## Rollout

Start with `observe` on the affected group, check the list for a few days for
legitimate entrypoints (for example `claude-desktop` or a new IDE extension),
add them, then switch to `enforce`.
