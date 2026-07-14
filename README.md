# SAIAI Server

[简体中文](README_CN.md)

SAIAI Server is a self-hosted AI API gateway with an administrative web UI.
It authenticates client API keys, routes supported provider protocols, applies
account selection and usage controls. The accompanying V2 rollout pull request
adds the schema-2 bootstrap contract used by the SAIAI V2 client.

This project began as a fork of Sub2API. It is an independent community
project: it is not an official service, website, or distribution of Sub2API or
any upstream AI provider. See [NOTICE](NOTICE) and
[source provenance](docs/SOURCE_PROVENANCE.md) for attribution.

## Repository boundaries

| Repository | Visibility | Responsibility |
| --- | --- | --- |
| `yuns2023/saiai-server` | Public | Gateway backend, admin frontend, database migrations, tests, release images, and the V2 bootstrap API |
| [`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client) | Public | The `saiai` CLI, Tauri desktop application, platform installers, and client release assets |
| `saiai-ops` | Private | Deployment-specific configuration, secrets, infrastructure inventory, backups, and operational records |

Client source and release binaries belong in `saiai-client`. Deployment
credentials and private infrastructure data must never be committed here.
This server keeps tested, origin-aware wrapper mirrors under
`scripts/saiai-cli/` and `frontend/public/saiai-cli/` so it can serve short
install commands from its own public origin; `saiai-client` remains the release
authority for client source and binaries.

## Highlights

- Provider-compatible gateway routes for supported Anthropic, OpenAI, and
  related account types
- API-key, group, account, quota, concurrency, rate-limit, and usage controls
- Sticky-session and account scheduling support
- Vue administrative interface for users, keys, groups, accounts, and
  observability
- PostgreSQL persistence and Redis-backed coordination
- A reviewed candidate for authenticated, non-billable SAIAI V2 bootstrap discovery

Support for a protocol or account type does not grant a right to use an
upstream service. Operators and users are responsible for provider terms,
account permissions, data handling, and applicable law.
Third-party OAuth client secrets are not distributed here. Compatibility OAuth
flows that require one remain disabled until an operator supplies a credential
they are authorized to use and accepts the provider's terms.
Sora watermark-free parsing has no built-in third-party endpoint. Requests must
provide an explicit custom `watermark_parse_url`; otherwise SAIAI does not
publish the generated post to a parser and follows the configured fallback
behavior.

## SAIAI V2

The schema-2 implementation is introduced by the initial public V2 rollout
pull request. Until that change is merged, the public `main` branch does not
promise schema-2 compatibility.

V2 is a clean client mode. Claude and Codex are optional, independently
configured products; configuring one must not request the other product's key.
Both products share one Gateway URL while keeping separate credentials and
isolated client homes.

The Gateway contract is:

```http
GET /api/v1/client/bootstrap
Authorization: Bearer <SAIAI API key>
```

Schema 2 distinguishes native Claude readiness from an OpenAI group's optional
`/v1/messages` protocol adapter. In particular,
`openai_messages_dispatch=true` never makes `capabilities.claude` true.
Bootstrap authenticates locally and does not select an upstream account or
send a model request.

See [V2 Gateway contract](docs/V2_GATEWAY_CONTRACT.md) for the normative
response shape, capability meanings, security properties, and release-pair
rules.

## Development

Prerequisites:

- Go version declared by `backend/go.mod`
- Node.js 20 and pnpm via Corepack
- PostgreSQL and Redis for integration and local runtime work

Common checks:

```bash
cd backend
go test -tags=unit ./...

cd ../frontend
corepack enable
pnpm install --frozen-lockfile
pnpm run lint:check
pnpm run typecheck
pnpm run test:run
```

Run targeted tests while iterating, then rely on the public CI matrix for the
full validation set. See [CONTRIBUTING.md](CONTRIBUTING.md) for the development
workflow and [deploy/](deploy/) for deployment templates. Review every example,
use unique secrets, pin release artifacts, and place the service behind HTTPS
before exposing it to untrusted networks.

## Security

Do not publish suspected vulnerabilities or credentials in an issue. Follow
[SECURITY.md](SECURITY.md) for private reporting and deployment guidance.

## License

The combined SAIAI Server work distributed by this repository is available
under the GNU Lesser General Public License, version 3 or any later version
(`LGPL-3.0-or-later`), except where a file or third-party notice says otherwise.
Incorporated portions received under MIT terms and other identified third-party
material are documented in [NOTICE](NOTICE); those notices remain in effect.
