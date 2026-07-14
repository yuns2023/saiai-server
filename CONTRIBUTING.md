# Contributing to SAIAI Server

Thank you for helping improve SAIAI Server. Keep contributions focused,
reviewable, and safe for a public repository.

## Before opening a change

- Search existing issues and pull requests before starting overlapping work.
- Use an issue for behavior changes that need design agreement.
- Report security problems privately according to [SECURITY.md](SECURITY.md).
- Never include API keys, OAuth tokens, cookies, private certificates, database
  dumps, production logs, private hostnames, or infrastructure addresses.
- Keep client CLI and desktop changes in
  [`saiai-client`](https://github.com/yuns2023/saiai-client). Keep deployment
  secrets and site-specific configuration outside this public repository.

## Development setup

Use the Go toolchain declared in `backend/go.mod`, Node.js 20, pnpm, PostgreSQL,
and Redis. Do not commit `.env`, generated credentials, or local runtime data.

```bash
corepack enable
pnpm --dir frontend install --frozen-lockfile
```

Backend and frontend can be checked independently:

```bash
cd backend
go test -tags=unit ./...
golangci-lint run ./...

cd ../frontend
pnpm run lint:check
pnpm run typecheck
pnpm run test:run
```

Integration tests may require PostgreSQL, Redis, and additional local setup.
Prefer the smallest relevant test during development. Before requesting review,
run all checks affected by the change and state which checks were not run.

If an Ent schema changes, regenerate and commit the generated output:

```bash
cd backend
go generate ./ent
go generate ./cmd/server
```

If frontend dependencies change, update and commit `frontend/pnpm-lock.yaml`.

## SAIAI V2 changes

Read [the V2 Gateway contract](docs/V2_GATEWAY_CONTRACT.md) before modifying
bootstrap authentication, capability computation, response fields, or release
behavior. Contract changes require tests for at least:

- unauthenticated rejection and `Cache-Control: no-store`;
- Anthropic/Antigravity Claude capability;
- OpenAI Codex capability;
- separation of `openai_messages_dispatch` from `claude`; and
- absence of account selection or upstream model calls during bootstrap.

Schema 2 is intentionally incompatible with the earlier preview. Do not add a
state-migration path or silently weaken capability checks to simulate backward
compatibility.

## Pull requests

- Create a topic branch and keep commits limited to one coherent change.
- Add or update tests for behavior changes.
- Update public documentation when a command, configuration field, endpoint, or
  operational assumption changes.
- Explain security and compatibility effects in the pull request description.
- Do not rely on real provider traffic in tests. Use local fakes or mock
  upstreams.
- Do not update release tags, packages, or production systems from a pull
  request.

By contributing, you agree that your contribution may be distributed under the
repository's applicable license, currently `LGPL-3.0-or-later`, while existing
third-party notices remain intact.
