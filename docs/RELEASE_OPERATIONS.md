# Public SAIAI release operations

This document describes repository-level mechanics. Exact production hosts,
image digests, manifest hashes, rollback coordinates, credentials and evidence
belong in the private Ops release ledger.

## Release boundaries

The local-proxy `1.1.0` cutover has two coordinated artifacts:

1. a Gateway image whose WebUI describes and generates the one-command local
   proxy setup and whose activation tooling accepts local-proxy candidates; and
2. the exact `saiai-v1.1.0` client bundle selected by manifest SHA-256.

Stage and validate the full client bundle before the change window. Activate
the reviewed bundle and deploy the reviewed Gateway digest only in the approved
paired transaction. Keep the previous Gateway digest and global-config client
bundle together as the exact rollback pair.

After this boundary is live, compatible local-proxy `1.1.x` client updates use
the short client-only path. They do not change Compose, recreate the Gateway,
stop `sub2api`, or require a maintenance page.

## Repository checks

This repository is maintained by one operator, so a pull request is optional.
An administrator may push a clean, linear commit directly to protected
`main`. All required workflows then run for that exact SHA. A direct push is
not production approval: the private release tool must observe every required
check succeeding before it accepts the automatically published SHA image.

`Docker Publish` runs in parallel with the required checks, publishes only the
immutable `sha-<commit>` candidate tag, and uploads a machine-readable
coordinate artifact. It does not repeat the backend test suite, publish a
mutable `main` tag, or require an operator to toggle a repository variable.
An image that finishes before CI is merely an ineligible candidate and must
never be deployed until the exact-SHA gate passes.

Run serially with bounded resources:

```bash
python3 scripts/saiai-cli/verify-client-gateway.py
bash scripts/saiai-cli/test-sync-saiai-cli.sh

cd backend
GOMAXPROCS=2 go test -p 1 -tags embed ./internal/web \
  -run '^(TestFrontendServer_Middleware|TestPublicRequestOrigin|TestNormalizePublicHost|TestNewFrontendServer)$' \
  -count=1

cd ../frontend
pnpm exec vitest run src/components/keys/__tests__/UseKeyModal.spec.ts \
  --maxWorkers=1 --minWorkers=1 --no-file-parallelism
```

The public client repository separately validates Rust tests and strict
Clippy, static Linux portability, Windows native behavior, all six assets,
wrapper no-download/repeat behavior, and the immutable manifest.

## Stage (networked, no live change)

Use an exact public tag and an independently recorded manifest hash:

```bash
scripts/sync-saiai-cli.sh stage saiai-v1.1.0 "$manifest_sha256"
```

Staging must not alter either live symlink. Record the resolved public source
commit, tag object, immutable release state, manifest hash, wrapper hashes and
six binary hashes in the private ledger.

## Coordinated contract cutover

Only after explicit production authorization:

1. Verify the staged bundle again without network access.
2. Confirm existing active and previous bundles pass retained-live validation
   and match the rollback coordinates in the private ledger.
3. Activate the exact `1.1.0` manifest:

   ```bash
   SAIAI_TEST_OFFLINE=1 scripts/sync-saiai-cli.sh activate \
     saiai-v1.1.0 "$manifest_sha256"
   ```

4. Deploy the reviewed Gateway image by immutable digest and recreate only the
   Gateway service covered by the approved plan.
5. Verify without a real model request:
   - health and retained bootstrap endpoints as applicable;
   - manifest and all wrapper hashes from the public URL;
   - wrapper origin rendering and `Cache-Control: no-store`;
   - WebUI shows one escaped command containing only a non-production test Key;
   - active/previous symlinks resolve to the recorded immutable bundles; and
   - unrelated API service remains available.

Do not log or paste a real user API Key as release evidence.

## Routine compatible client update (short path)

For a later compatible local-proxy release:

```bash
scripts/sync-saiai-cli.sh stage saiai-v1.1.x "$manifest_sha256"
SAIAI_TEST_OFFLINE=1 scripts/sync-saiai-cli.sh activate \
  saiai-v1.1.x "$manifest_sha256"
```

Then fetch `manifest.json` and the three wrappers from the public URL and
compare their hashes with the ledger. Activation only replaces symlinks; it
does not touch the Gateway process. No service lock or maintenance window is
required beyond the script's short filesystem lock, which prevents two
operators from changing the same symlink concurrently.

## Rollback

For a compatible local-proxy client-only regression, activate the exact
previous local-proxy bundle by its recorded tag and manifest hash. Do not use
`latest`, reconstruct hashes from memory, or restore the database.

If the regression crosses the `1.1.0` UI/client contract boundary, roll back
the reviewed Gateway digest and matching global-config client bundle as one
pair using the private runbook. Routine local-proxy activation intentionally
refuses a global-config or V2 target. The old pair must remain staged until the
user explicitly authorizes retirement.

Post-rollback checks remain non-billable; never use a real model request as a
smoke test.
