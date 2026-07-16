# Public SAIAI release operations

This document describes repository-level mechanics. Exact production hosts,
image digests, manifest hashes, rollback coordinates, credentials and evidence
belong in the private Ops release ledger.

## Release boundaries

The initial global-config `1.0.0` cutover has two coordinated artifacts:

1. a Gateway image whose WebUI generates one-command global configuration and
   whose `/saiai-cli/*` handler serves the full external bundle; and
2. the exact `saiai-v1.0.0` client bundle selected by manifest SHA-256.

Stage and validate the client bundle before recreating the Gateway service.
Activate the reviewed bundle only in the approved cutover transaction. Keep the
previous server digest and previous client bundle available for rollback.

Once that server boundary is live, compatible `1.x` client updates are a short,
client-only path. They do not change Compose, recreate the Gateway, stop
`sub2api`, or require a maintenance page.

## Repository checks

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

The public client repository separately validates Rust tests/clippy, static
Linux portability, Windows native behavior, all six assets, wrapper no-download
behavior, and the immutable manifest.

## Stage (networked, no live change)

Use an exact public tag and an independently recorded manifest hash:

```bash
scripts/sync-saiai-cli.sh stage saiai-v1.0.0 "$manifest_sha256"
```

Staging is safe before the change window. It must not alter either live symlink.
Record the resolved public source commit, tag object, release state, manifest
hash, wrapper hashes and six binary hashes in the private ledger.

## Initial coordinated cutover

Only after explicit production authorization:

1. Verify the staged bundle again without network access.
2. Confirm the existing active V2 bundle passes retained-live validation and is
   the exact rollback bundle from the private ledger.
3. Activate the exact `1.0.0` manifest:

   ```bash
   SAIAI_TEST_OFFLINE=1 scripts/sync-saiai-cli.sh activate \
     saiai-v1.0.0 "$manifest_sha256"
   ```

4. Deploy the reviewed Gateway image by immutable digest and recreate only the
   Gateway service covered by the approved plan.
5. Verify, without a real model request:
   - health/bootstrap public endpoints as applicable;
   - manifest and all wrapper hashes from the public URL;
   - wrapper origin rendering and `Cache-Control: no-store`;
   - WebUI shows one command containing a non-production test Key;
   - active/previous symlinks resolve to the recorded immutable bundles; and
   - unrelated API service remains available.

Do not log or paste a real user API Key as release evidence.

## Routine compatible client update (short path)

For a later compatible `1.x` release:

```bash
scripts/sync-saiai-cli.sh stage saiai-v1.x.y "$manifest_sha256"
SAIAI_TEST_OFFLINE=1 scripts/sync-saiai-cli.sh activate \
  saiai-v1.x.y "$manifest_sha256"
```

Then fetch `manifest.json` and the three wrappers from the public URL and compare
their hashes with the ledger. Activation only replaces symlinks; it does not
touch the Gateway process. No service lock or maintenance window is required
beyond the script's short filesystem lock, which prevents two operators from
changing the same symlink concurrently.

## Rollback

For a compatible client-only regression, activate the exact previous bundle by
its recorded tag and manifest hash. Do not use `latest`, reconstruct hashes from
memory, or restore the database.

If the regression crosses the initial UI/wrapper contract boundary, roll back
the reviewed Gateway digest and matching previous client bundle as a pair. The
old pair must remain staged until the user explicitly authorizes retirement.
The global-config `activate` action deliberately refuses a V2 target; use the
exact retained V2 activation procedure recorded with the rollback pair while
maintenance is active.

Post-rollback checks remain non-billable; never use a real model request as a
smoke test.
