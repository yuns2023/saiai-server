# SAIAI client delivery contract

## Current public client mode

The current SAIAI Client uses `local-proxy` mode with manifest schema 1 and
configuration schema 1. Resolve its exact patch version, tag, source commit,
and manifest hash from the current private Ops ledger rather than this stable
contract. The Claude one-command path
installs or reuses one small binary, updates the managed Claude Code settings,
creates or reuses a per-user installation CA, and starts or refreshes a
per-user background proxy. Users then launch `claude` normally, including from
VSCode. `saiai`, `saiai start`, `saiai stop`, `saiai status`, `saiai logs`,
`saiai restart`, and `saiai doctor` remain available for direct operation.

The setup command is repeatable. It always applies the supplied Gateway and
Key, but it skips the binary download when the installed file already has the
manifest hash. It preserves unrelated Claude settings, removes managed legacy
authentication/proxy/CA conflicts, removes stale OAuth account state and
credentials, and reuses a valid installation CA. The CA private key is
generated locally, stored with user-only permissions, and is never a release
asset.

The Codex path remains independent: it merges Codex configuration and does not
start the Claude local proxy. Neither path calls `/api/v1/client/bootstrap`.
The public source and release assets live in
[`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client). This
Gateway repository owns the WebUI instructions, immutable bundle activation,
and HTTP serving boundary.

## Manifest and bundle

Every release contains exactly the six fixed binaries, three wrappers, and one
manifest. The manifest header is:

```json
{
  "manifest_schema": 1,
  "client_mode": "local-proxy",
  "configuration_schema_version": 1,
  "version": "1.1.x"
}
```

`assets` and `wrappers` contain the exact SHA-256 and size of every file. A
local-proxy manifest must not claim `bootstrap_schema_version` compatibility.
The six binary names remain:

- `saiai-linux-x86_64`
- `saiai-linux-aarch64`
- `saiai-macos-x86_64`
- `saiai-macos-aarch64`
- `saiai-windows-x86_64.exe`
- `saiai-windows-aarch64.exe`

Linux release assets are static binaries so they do not inherit a recent glibc
requirement from the build runner.

## Gateway serving and WebUI boundary

`SAIAI_CLIENT_DIR` points at one immutable, validated bundle. Gateway serves the
manifest, all binaries, and all three wrappers from that directory. There is no
embedded-wrapper fallback: a missing wrapper returns `503` instead of combining
files from different client releases. Missing-bundle responses are
end-user-facing: they provide retry/contact-administrator guidance without
exposing private filesystem paths or operator commands. WebUI-generated
PowerShell/CMD commands stay as one-line legacy commands; wrapper/native output
is surfaced directly instead of adding a second UI error wrapper.

Wrapper responses replace only the literal default
`https://api.saiai.top/saiai-cli` with the trusted public request origin. The
origin boundary never trusts `X-Forwarded-Host` or `Forwarded`; it accepts
`X-Forwarded-Proto` only from configured trusted proxies. All `/saiai-cli/*`
responses use `Cache-Control: no-store`.

The normal Codex WebUI command is the short `init-codex <base_url> <api_key>`
form, and Claude uses the equivalent short `<base_url> <api_key>` form. Both
include the selected Gateway and API Key, so each shell must quote the Key
correctly. The WebUI exposes only the normal Codex CLI path; WebSocket-specific
setup is not exposed. Tests use only `TEST_ONLY_*` values and assert the
commands stay concise while escaping apostrophes correctly. Release logs and
ledgers must never contain real user keys.

The OpenAI API Key setup UI offers one Codex CLI command. It does not expose a
separate WebSocket setup tab; the Client's explicit `init-codex --websockets`
option remains available for compatibility.

Post-setup guidance is separate from the one-command configuration. With a
compatible active Client, it retains normal Claude/VSCode use, describes
`saiai claude` only as child-environment recovery for inherited configuration
conflicts, and lists `saiai codex`, `saiai desktop codex` and VSCode restart
instructions. It does not add a mode selector, run a command in the browser or
claim that copying completed setup. The recovery launcher respects explicit
user/project settings and does not repair the VSCode host environment; Desktop
guidance covers Codex features, not ordinary ChatGPT. These advertised entrypoints
must be supported by the exact Client bundle selected for the release. This does
not restore withdrawn V2 setup/runtime/bootstrap behavior or permit a mismatched
Gateway/Client release.

## Activation

`scripts/sync-saiai-cli.sh stage <tag> <manifest-sha256>` is the only networked
operation. It downloads the exact GitHub release, validates its immutable file
set, local-proxy manifest contract, hashes, sizes, ownership and permissions,
and stores it under a content-addressed directory without changing live links.

`scripts/sync-saiai-cli.sh activate <tag> <manifest-sha256>` is offline. It
revalidates the staged bundle under a short shared filesystem lock and replaces
the `previous` and `active` symlinks atomically. It does not recreate or signal
the Gateway process and does not interrupt API traffic.

Live-link validation recognizes exact hash-valid local-proxy schema-1,
retained global-config schema-1, and retained V2 schema-2 bundles. This permits
the reviewed current pair to become the rollback pair during a contract
cutover. Stage and activate targets remain local-proxy only; the retained-live
exception cannot publish an older contract as a routine client-only change.

The `1.1.0` cutover is a coordinated server/client change because the WebUI
changes from direct global configuration to the background local proxy and the
activation candidate contract changes with it. After that cutover, a compatible
local-proxy `1.1.x` update normally needs only stage, validate, and activate; no
Compose change or Gateway restart is required. For an explicitly accepted
forward-only client-only release, the local predecessor may be pruned after
postflight while the immutable remote Release and ledger coordinates remain
available for an emergency re-stage.

## Retained bootstrap endpoint

The Gateway may retain `/api/v1/client/bootstrap` schema 2 for compatibility
with older clients during an explicitly chosen transition window. It remains
API-key authenticated, non-cacheable, non-billable, and must not select an
upstream account or issue a model request. OpenAI groups expose native Responses
only; bootstrap does not advertise Claude Messages dispatch for them.

An API Key bound to a non-active group is rejected before bootstrap handling,
the same as every other API-key route. Disabling a group therefore prevents its
issued Keys from using bootstrap metadata as well as model and usage endpoints.

The local-proxy client neither calls nor depends on this endpoint. Retaining
the endpoint does not make `1.1.0` a V2 client and does not permit its manifest
to claim bootstrap compatibility.

## Codex local-proxy-only group policy

OpenAI groups may set `codex_client_policy=local_proxy_only` to reject the
legacy `base_url`/API-key route while allowing the current SAIAI local-proxy
OAuth shape. The request gate applies only to Codex model ingress
(`/v1/responses`, Responses WebSocket and the Codex models manifest); ChatGPT
Desktop/VSCode control-plane sidecars are not subject to it.

The gate requires a recognized Codex product/version at the start of the
`User-Agent`, together with non-empty `chatgpt-account-id` and `version`
headers. A missing UA, a non-Codex UA (including curl), an embedded Codex
substring, or a missing product version is rejected even when `originator`
claims Codex. A present `originator` must match the exact allowlist:
`codex_cli_rs`, `codex-tui`, `codex_vscode`, `codex_app`, `codex_chatgpt_desktop`,
`codex_work_desktop`, `codex_atlas`, `codex_exec`, `codex_sdk_ts`, or `Codex Desktop`
(case-insensitive). Official UAs without `originator` remain accepted;
app-server UAs may carry a different allowlisted surface originator.

The official interactive terminal in Codex 0.154.0 and 0.160.0 uses the
`codex-tui/<version>` User-Agent and `codex-tui` originator through its
app-server. Model discovery may initially pair that UA with `codex_cli_rs`.
Both native shapes are recognized without rewriting client identity or
relaxing the version/account requirements on Responses. Terminal admission
includes this exact product; embedded products and unknown suffixes remain
rejected.

The Windows Desktop work surface uses `codex_work_desktop/<version>` and the
exact `codex_work_desktop` originator. Recognize this native shape without
rewriting it to a terminal identity. Responses still requires the account and
version headers, unknown product/originator suffixes remain rejected, and the
work Desktop UA does not qualify for `cli_only`.

The same client-header admission applies to the `official_clients` and
`cli_only` group policies, OAuth account restrictions, and native relay
accounts, across HTTP and WebSocket. `cli_only` additionally requires the
terminal/exec UA; an originator cannot convert a VSCode UA into a terminal UA.
The models catalog retains its existing exception for missing `version`, but
still requires valid client headers and non-empty `chatgpt-account-id` under
`local_proxy_only`. Group policy rejection logs contain only correlation,
policy and reason fields, without raw headers, account IDs or request bodies.

This is an operational migration
signal, not cryptographic attestation: it is intended to catch stale
`config.toml`/`init-codex` configurations and reject contradictory client
headers. A caller able to reproduce all allowed request fields can still
imitate the accepted shape. Validate each CLI, Desktop and IDE
version before enabling the policy for a production group. The default remains
`off` and older clients must not be rejected until the updated client bundle is
available.

## Codex Responses account-switch boundary

Completed HTTP and WebSocket Responses bind their provider-issued response ID to
the requesting SAIAI user and selected upstream account. An official Codex
OAuth request with `previous_response_id` is sent only to that user's bound
account. An unknown binding or an ordinary switch to another account returns a
conversation-restart error before provider egress.

Native OAuth quota failover may send a fresh, uncommitted WebSocket request to
another compatible account only when no output has reached the client and no
account-bound continuation or input is present. The replay keeps the original
application message bytes. A previous response anchor, encrypted reasoning,
item reference, tool output or turn-state blocks account migration; the Gateway
does not remove these fields or rebuild a reduced conversation history. The
provider failure is returned so Codex can choose its next request. API-key
compatibility recovery remains a separate path.

The provider-facing `session_id` and `conversation_id`, including hyphenated
aliases, preserve the client's exact values, multiplicity and presence. User
and account isolation belongs to internal ownership keys. A supplied
`x-codex-turn-state` is forwarded unchanged only when its user/account ownership
can be verified; missing state remains absent, and unknown state fails before
provider egress. Ownership is observed from response headers and native
`response.metadata` events without changing those event payloads. The official
Codex `client_metadata` body remains unchanged; the Gateway does not replace it
with a reduced turn-metadata header. See
[`OPENAI_OAUTH_NATIVE_PASSTHROUGH.md`](OPENAI_OAUTH_NATIVE_PASSTHROUGH.md) for the
full preservation contract, allowed per-hop differences and versioned evidence.

SAIAI Keys and groups do not define the upstream state namespace. The same user
may continue through another Key or group when the bound upstream account is
also schedulable in the new group. Current-group account membership remains a
hard routing constraint; a cached owner never makes an unavailable or unbound
account eligible in that group.

Existing official Codex OAuth continuations without a recorded response-to-account binding may
need a fresh Codex conversation after activation. This is intentional: the
Gateway cannot establish which upstream account issued an unknown response ID.

## Experimental native ChatGPT Chat ingress

Ordinary ChatGPT Desktop Chat is not the Responses protocol. The experimental
ingress is namespaced under `/chatgpt/backend-api/*` and remains disabled by
default through `gateway.openai_chat_enabled=false`. It accepts only an
explicit route allowlist and only schedules OpenAI OAuth accounts. The Gateway
removes the client Authorization/Cookie/account ID, substitutes the selected
account OAuth token and account ID, preserves the native body/path/query and
client identity headers, and streams the upstream event response without
converting it to Responses. `Set-Cookie` and hop-by-hop response headers are
not returned to the client.

The same experimental namespace forwards the ordinary Chat model catalog
`GET /chatgpt/backend-api/models` with its native schema, path and raw query.
It is independent from the Codex `/v1/models` catalog. Initialization,
catalog and asset requests are control requests, not model turns. Account
type eligibility is enforced before reserving a concurrency slot; a pool
containing only API-key accounts fails without reserving those accounts.

The namespace also includes the Desktop file-asset resolver
`GET /chatgpt/backend-api/files/download/{file_id}` and the signed asset-byte
path `GET /chatgpt/backend-api/estuary/content`. The first is a control-plane
request: the Gateway selects the account bound to the optional
`conversation_id`, substitutes Gateway-owned OAuth authentication, and passes
through the provider's short-lived JSON result (`download_url`, `retry`, or
`error`) without counting a model turn. The second preserves the provider's
binary response. Current Desktop-signed URLs expose an opaque provider `cid`,
not the conversation UUID; it is preserved in the upstream query and is not
used to create a conversation binding. The resolver has been validated with
one active staging account. Do not treat that value as multi-account conversation affinity. A
native Chat implementation is not complete for image generation unless both
resolver paths are available end to end, and production multi-account use
still requires an explicit file-to-account binding.

`gateway.openai_chat_upstream_base_url` is empty in normal operation. An
isolated staging stack may point it at a replay/fake provider so the ingress,
SSE flushing, and request-shape invariants can be tested without contacting a
real model. Do not enable the public route against `chatgpt.com` until token,
session/cookie requirements, multi-turn affinity, accounting, and upstream
error behavior have separate credentialed-staging evidence.

For an explicitly authorized credentialed-staging window,
`gateway.openai_chat_model_request_cap` places a process-lifetime hard limit on
final `/f/conversation` upstream attempts; `init`, `prepare`, and Sentinel
control-plane calls do not consume that limit. Recreate the isolated Gateway
immediately before the window, set the approved cap, and restore the replay
upstream immediately afterward. A zero cap disables this safeguard and must
not be used for a limited live probe.

`gateway.openai_chat_success_turn_price_usd` defaults to zero and
`gateway.openai_chat_unaccounted_allowed` defaults to false. With both defaults,
final `/f/conversation` requests are rejected before account selection or
upstream traffic. A positive finite price enables fixed successful-turn
billing; only a 2xx response with an observed `message_stream_complete` and no
provider error costs one turn. The usage row retains the native requested/observed model and thinking effort;
it reports unavailable token usage and charges the admitted tier price through
the existing multiplier, subscription/balance, quota and idempotent billing
transaction. Pro handoff/resume completion settles the original user message
once using its shared accounting snapshot. See the native accounting contract
for the price table, ownership and rollback rules.

Final native Chat turns retain request-context values but detach upstream
cancellation from a downstream disconnect so the Gateway can observe the real
terminal result. `gateway.openai_chat_turn_timeout_seconds` defaults to 600 and
bounds that drain; a request cancelled before forwarding is not sent.

Only an isolated replay or explicitly capped credentialed-staging stack may
set `gateway.openai_chat_unaccounted_allowed=true`. Control-plane
`init`/`prepare` requests remain available for protocol research, but this flag
must never be enabled as a production substitute for accounting.

The versioned native-Chat accounting rules and activation gates are defined in
[`CHATGPT_NATIVE_ACCOUNTING_CONTRACT.md`](CHATGPT_NATIVE_ACCOUNTING_CONTRACT.md).
In particular, the Desktop thread-usage endpoint returns a cumulative,
eventually-consistent conversation snapshot; parsing it does not make it safe
to pass directly to request-level billing.

Ops records the requested model before native Chat or WebSocket account
selection fails. A control request with no model carries no synthesized model.
After an HTTP 101 upgrade or SSE keepalive, a local terminal rejection is still
recorded with its semantic error status, the original transport status and,
for WebSockets, the close code. This does not change the wire status, close
frame, upstream payload or stream completion events.

`gateway.openai_chat_response_shape_capture` is a default-off, staging-only
diagnostic. It may record protocol field names but never field values or
message content and must be disabled immediately after the authorized window.
