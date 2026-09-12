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
PowerShell/CMD commands keep wrapper download and `Invoke-Saiai` inside one
error boundary so a failed download cannot cascade into an undefined-function
error.

Wrapper responses replace only the literal default
`https://api.saiai.top/saiai-cli` with the trusted public request origin. The
origin boundary never trusts `X-Forwarded-Host` or `Forwarded`; it accepts
`X-Forwarded-Proto` only from configured trusted proxies. All `/saiai-cli/*`
responses use `Cache-Control: no-store`.

The WebUI command contains the selected API Key by explicit product design. It
must quote the Key separately for POSIX shell and PowerShell. Tests use only
`TEST_ONLY_*` values and assert both that the command contains the Key and that
apostrophes are escaped correctly. Release logs and ledgers must never contain
real user keys.

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

The local-proxy client neither calls nor depends on this endpoint. Retaining
the endpoint does not make `1.1.0` a V2 client and does not permit its manifest
to claim bootstrap compatibility.

## Codex local-proxy-only group policy

OpenAI groups may set `codex_client_policy=local_proxy_only` to reject the
legacy `base_url`/API-key route while allowing the current SAIAI local-proxy
OAuth shape. The request gate applies only to Codex model ingress
(`/v1/responses`, Responses WebSocket and the Codex models manifest); ChatGPT
Desktop/VSCode control-plane sidecars are not subject to it.

The gate requires an official Codex client family together with non-empty
`chatgpt-account-id` and `version` headers. This is an operational migration
signal, not cryptographic attestation: it is intended to catch stale
`config.toml`/`init-codex` configurations. Validate each CLI, Desktop and IDE
version before enabling the policy for a production group. The default remains
`off` and older clients must not be rejected until the updated client bundle is
available.

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

The same experimental namespace includes the Desktop file-asset resolver
`GET /chatgpt/backend-api/files/download/{file_id}` and the signed asset-byte
path `GET /chatgpt/backend-api/estuary/content`. The first is a control-plane
request: the Gateway selects the account bound to the optional
`conversation_id`, substitutes Gateway-owned OAuth authentication, and passes
through the provider's short-lived JSON result (`download_url`, `retry`, or
`error`) without counting a model turn. The second preserves the provider's
binary response. Current Desktop-signed URLs expose an opaque provider `cid`,
not the conversation UUID; the present implementation uses it only as a
best-effort scheduler hash and has been validated with one active staging
account. Do not treat that value as multi-account conversation affinity. A
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
provider error costs one turn. The usage row records zero tokens under the
stable `chatgpt-native-turn` model and charges the configured base price through
the existing multiplier, subscription/balance, quota, and idempotent billing
transaction.

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

`gateway.openai_chat_response_shape_capture` is a default-off, staging-only
diagnostic. It may record protocol field names but never field values or
message content and must be disabled immediately after the authorized window.
