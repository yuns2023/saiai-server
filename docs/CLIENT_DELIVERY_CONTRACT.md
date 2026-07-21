# SAIAI client delivery contract

## Current public client mode

SAIAI Client `1.1.0` is a `local-proxy` client. The Claude one-command path
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
  "version": "1.1.0"
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
files from different client releases.

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
upstream account or issue a model request. `openai_messages_dispatch` remains
protocol compatibility and does not imply native Claude capability.

The local-proxy client neither calls nor depends on this endpoint. Retaining
the endpoint does not make `1.1.0` a V2 client and does not permit its manifest
to claim bootstrap compatibility.
