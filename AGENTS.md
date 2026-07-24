# Public repository agent guide

This file contains development guidance only. Production topology, credentials,
deployment records, and private operational procedures do not belong in this
repository.

## Repository boundaries

- `backend/`: Go gateway, API, persistence, and embedded-web integration.
- `frontend/`: Vue administrative interface; use pnpm and keep the lockfile in
  sync.
- `deploy/`: generic self-hosting templates, never site-specific values.
- `docs/`: public protocols and contributor-facing design records.
- SAIAI CLI and Tauri desktop source belongs in the public `saiai-client`
  repository.
- Deployment secrets and infrastructure inventory belong outside this public
  repository.

## Required reading

Before changing `/api/v1/client/bootstrap`, capability reporting, or SAIAI
client delivery, read
[docs/CLIENT_DELIVERY_CONTRACT.md](docs/CLIENT_DELIVERY_CONTRACT.md).
Preserve these invariants:

- schema version is 2;
- bootstrap requires normal API-key authentication and is non-cacheable;
- bootstrap performs no upstream account selection or model request;
- Claude and Codex capabilities are independent;
- OpenAI Messages dispatch is protocol compatibility only and never implies
  native Claude capability; and
- the local-proxy client does not call bootstrap or claim bootstrap schema
  compatibility;
- its per-user CA private key is generated locally and is never shipped as a
  release asset; and
- the local-proxy contract cutover keeps the reviewed previous Gateway image
  and global-config client bundle as one rollback pair.

Before changing OpenAI service-tier billing or its user/admin presentation,
read the service-tier section in
[docs/API_KEY_USAGE_ANALYTICS.md](docs/API_KEY_USAGE_ANALYTICS.md). Keep the
HTTP, WebSocket and WebSocket v2 billing resolver aligned.

## Change discipline

- Preserve unrelated work in a dirty tree.
- Prefer targeted searches with `rg` and narrow tests while iterating.
- Do not run real provider requests as tests. Use local mock upstreams.
- Do not print or commit secrets, private certificates, captures, database
  exports, machine-specific paths, or internal host details.
- Preserve unrelated JSON/configuration fields when implementing mutations.
- Add tests for behavior changes and update documentation when public contracts
  change.
- Do not mutate releases, packages, repositories, or deployments unless the
  task explicitly authorizes that external action.

## Validation

Run the smallest checks that cover the change first:

```bash
cd backend
go test -tags=unit ./path/to/affected/package

cd ../frontend
pnpm run lint:check
pnpm run typecheck
pnpm run test:run
```

Avoid running resource-heavy full backend checks concurrently on a shared
machine. Public CI provides the full matrix. Report exactly what was and was not
run.
