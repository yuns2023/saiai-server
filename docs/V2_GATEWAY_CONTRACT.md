# SAIAI V2 Gateway contract

Status: schema 2 contract for SAIAI CLI `0.9.0` and compatible clients.

This document defines the public boundary between SAIAI Server and the clean
SAIAI V2 client. Client implementation and installers live in
[`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client).

## Endpoint

```http
GET /api/v1/client/bootstrap
Authorization: Bearer <SAIAI API key>
```

The request uses normal Gateway API-key authentication. A missing, malformed,
expired, disabled, or otherwise unusable key is rejected by the normal
authentication policy. Successful responses include:

```http
Cache-Control: no-store
Vary: Authorization
```

A conforming bootstrap implementation authenticates and evaluates the key's
current group locally. It must not select an upstream account, forward a
provider request, or consume model quota.

## Successful response

```json
{
  "code": 0,
  "message": "success",
  "data": {
    "schema_version": 2,
    "gateway_version": "...",
    "capabilities": {
      "claude": true,
      "codex": false,
      "codex_responses": false,
      "codex_websockets": false,
      "openai_messages_dispatch": false
    }
  }
}
```

`gateway_version` is informational. Compatibility decisions use
`schema_version` and explicit capability fields.

## Capability semantics

| Field | Meaning |
| --- | --- |
| `claude` | The authenticated key's active group can initialize and run the managed Claude product through a native supported route. Anthropic and compatible Antigravity groups may report this capability. |
| `codex` | The active group can initialize and run the managed Codex product. This is reported for an eligible OpenAI group. |
| `codex_responses` | Codex can use the HTTPS Responses API through this group. |
| `codex_websockets` | The Gateway can promise Codex WebSocket transport without further account selection. The initial schema-2 release paired with client `0.9.0` reports `false`; Codex uses HTTPS Responses. |
| `openai_messages_dispatch` | The OpenAI group has optional `/v1/messages` protocol dispatch enabled. This is protocol compatibility, not native Claude-product readiness. |

The following invariant is normative:

```text
openai_messages_dispatch == true  does not imply  claude == true
```

An OpenAI group can therefore return `codex=true`, `codex_responses=true`,
`openai_messages_dispatch=true`, and `claude=false`. V2 Claude setup must check
only `capabilities.claude`; it must not use Messages dispatch as a substitute.

An unconfigured, inactive, missing, or ineligible group returns false
capabilities where authentication policy permits a successful response. The
client treats a false selected-product capability as an incompatibility and
does not write a partial configuration.

## V2 client behavior

Claude and Codex are optional, independent products:

- `saiai claude` initializes only Claude on its first interactive launch;
- `saiai codex` initializes only Codex on its first interactive launch;
- interactive automation reads the selected product credential from stdin with
  `saiai setup claude --base-url <url> --api-key-stdin` or
  `saiai setup codex --base-url <url> --api-key-stdin`;
- bare `saiai setup` asks for a product before reading a credential;
- the user is never asked for both product keys merely because one product is
  being configured; and
- both configured products must use one normalized Gateway URL, while each
  keeps its own credential reference and isolated client generation.

V2 does not read, import, migrate, repair, or delete legacy SAIAI, Claude, or
Codex configuration. Obsolete or corrupt V2 state is recovered with
`saiai revoke --all`, followed by fresh per-product setup. Product revoke
removes only that product and must leave the other product usable.

Credentials must not appear in non-secret configuration, command arguments,
normal logs, diagnostic output, desktop events, or bootstrap responses.
There is intentionally no `--api-key <value>` form: unattended callers must
opt in to `--api-key-stdin` and supply the selected product's credential only.

## Version and release compatibility

Schema 2 is a clean break from the earlier all-or-nothing preview schema. A V2
client must require schema 2; it must not guess compatibility from
`gateway_version` or attempt a schema-1 migration.

Within schema 2, new capability fields may be additive. Clients must ignore
unknown response fields, but a server must not change the meaning of an
existing field without a new schema version.

The Gateway and the matching V2 client manifest/binary bundle form one release
pair. An operator must not publicly expose:

- a schema-2 Gateway with a schema-1 client bundle; or
- a V2 `0.9.0` bundle with a schema-1 Gateway.

Stage and verify both sides before switching public install/bootstrap traffic.
Rollback also restores both sides as a pair. Release records should bind the
Gateway image digest to the client manifest hash without recording credentials.
The deployment-neutral [release operations runbook](RELEASE_OPERATIONS.md)
defines the required maintenance, validation, and rollback sequence.

The verified client coordinates for the initial schema-2 rollout are:

- release authority: `yuns2023/saiai-client`;
- release tag: `saiai-v0.9.0`;
- client source commit: `abbc0e425efe4101f7180da892aaf80672bf21b6`; and
- manifest SHA-256:
  `092107c40b60cf0174e7278891fbb3cb097ccbe7cc05e8bef05e411687dfa02a`.

From the repository root, stage and activate that exact bundle with the same
independently recorded hash:

```bash
manifest_sha256=092107c40b60cf0174e7278891fbb3cb097ccbe7cc05e8bef05e411687dfa02a
SAIAI_CLIENT_DIR=/var/lib/saiai-server/client-runtime/saiai-cli \
  scripts/sync-saiai-cli.sh stage saiai-v0.9.0 "$manifest_sha256"
SAIAI_CLIENT_DIR=/var/lib/saiai-server/client-runtime/saiai-cli \
  scripts/sync-saiai-cli.sh activate saiai-v0.9.0 "$manifest_sha256"
```

`stage` is the only networked operation. `activate` is offline and re-verifies
the staged bundle. Do not activate a tag with a different hash, and record the
Gateway image digest beside these client coordinates before deployment.

## Contract tests

Server changes should keep automated coverage for:

1. unauthenticated rejection with non-cacheable response headers;
2. no reflection of the submitted API key;
3. Anthropic/Antigravity native Claude capability;
4. OpenAI Codex and Responses capabilities;
5. strict separation of OpenAI Messages dispatch from Claude capability;
6. conservative WebSocket reporting; and
7. bootstrap execution without account-selection or upstream transport
   dependencies.
