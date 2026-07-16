# SAIAI client delivery contract

## Current public client mode

SAIAI Client `1.0.0` is a `global-config` client. It installs one small binary
and writes user-level Claude Code or Codex CLI configuration. It does not launch
the official clients, create isolated homes or generations, run a local proxy,
install a CA, or call `/api/v1/client/bootstrap`.

The public client source and release assets live in
[`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client). This Gateway
repository owns only the WebUI instructions, immutable bundle activation, and
HTTP serving boundary.

## Manifest and bundle

Every release contains exactly the six fixed binaries, three wrappers, and one
manifest. The manifest header is:

```json
{
  "manifest_schema": 1,
  "client_mode": "global-config",
  "configuration_schema_version": 1,
  "version": "1.0.0"
}
```

`assets` and `wrappers` contain the exact SHA-256 and size of every file. A
global-config manifest must not claim `bootstrap_schema_version` compatibility.

The six binary names remain:

- `saiai-linux-x86_64`
- `saiai-linux-aarch64`
- `saiai-macos-x86_64`
- `saiai-macos-aarch64`
- `saiai-windows-x86_64.exe`
- `saiai-windows-aarch64.exe`

## Gateway serving boundary

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
set, manifest contract, hashes, sizes, ownership and permissions, and stores it
under a content-addressed directory without changing live links.

`scripts/sync-saiai-cli.sh activate <tag> <manifest-sha256>` is offline. It
revalidates the staged bundle under a short shared filesystem lock and replaces
the `previous` and `active` symlinks atomically. It does not recreate or signal
the Gateway process and does not interrupt API traffic.

For the initial cutover only, live-link validation also recognizes an exact,
hash-valid retained V2 schema-2 bundle so the current deployment can become the
rollback target. Staging and activation targets remain global-config only; this
exception cannot publish or activate a V2 target under the new contract.

The first `1.0.0` cutover is a coordinated server/client change because the
Gateway begins sourcing wrappers from the active directory and the WebUI begins
generating global-config arguments. After that cutover, a compatible `1.x`
client-only update normally needs only stage, validate, and activate; no Compose
change or Gateway restart is required.

## Retained bootstrap endpoint

The Gateway may retain `/api/v1/client/bootstrap` schema 2 for API compatibility
with older clients during an explicitly chosen transition window. It remains
API-key authenticated, non-cacheable, non-billable, and must not select an
upstream account or issue a model request. `openai_messages_dispatch` remains
protocol compatibility and does not imply native Claude capability.

The global-config client neither calls nor depends on this endpoint. Retaining
the endpoint does not make the `1.0.0` release a V2 client and does not permit a
global-config manifest to claim bootstrap compatibility.
