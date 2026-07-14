# Release operations

This runbook defines the public, deployment-neutral procedure for releasing
SAIAI Server and a matching SAIAI V2 client bundle. Keep site addresses,
credentials, infrastructure inventory, and backup locations in a private
operations repository.

Command examples assume Bash with `set -euo pipefail`. Define every coordinate
from the private copy of
[`deploy/release-pair.env.example`](../deploy/release-pair.env.example) before
using them; review the commands rather than pasting the runbook wholesale.

## Safety contract

A V2 release is one compatibility pair:

- one protected SAIAI Server source commit and immutable image digest; and
- one `saiai-client` tag and exact manifest SHA-256.

Never expose schema 2 with a schema-1 client bundle, or client `0.9.0` with a
schema-1 Gateway. Switch and roll back both sides as a pair.

Additional release rules:

- use a clean worktree based on the latest protected `main`;
- run targeted local tests serially and let GitHub Actions run the full matrix;
- deploy image digests, never mutable tags;
- stage client assets before the maintenance window and activate them offline;
- recreate only the application service, not PostgreSQL or Redis;
- keep credentials out of command arguments, logs, release records, and
  Compose files; and
- do not send a real model request as a deployment smoke test.

## Release coordinates

Copy [`deploy/release-pair.env.example`](../deploy/release-pair.env.example) to
the private operations repository and fill every required value. It is a
coordinate record, not a secrets file. Use absolute paths for the retained
current and previous activation scripts and Compose files.

At minimum, record:

| Coordinate | Requirement |
| --- | --- |
| Server source | Full protected `main` commit SHA |
| Server image | `ghcr.io/...@sha256:<digest>` |
| Client source | Full `saiai-client` source commit |
| Client release | Exact `saiai-v<version>` tag |
| Client manifest | Independently recorded 64-character SHA-256 |
| Activation script | SHA-256 of `scripts/sync-saiai-cli.sh` |
| Previous pair | Previous server source/image and client source/tag/manifest |

The initial schema-2 client coordinates remain documented in the
[V2 Gateway contract](V2_GATEWAY_CONTRACT.md). A future release must record its
own coordinates rather than silently reusing those values.

## State machine

Treat a production release as the following state machine:

```text
prepared -> quiesced -> backed_up -> activated -> locally_verified
                                              |-> rolled_back
locally_verified -> public_verified -> complete
                                  |-> maintenance -> rolled_back
```

Each transition needs a positive check. If a check fails, keep maintenance in
place and either correct the current stage or restore the previous pair. Do not
continue with a half-updated deployment.

## 1. Prepare and validate source

Before merging the release change:

1. Confirm all required `main` checks are enabled, including:
   - backend tests and `golangci-lint`;
   - frontend lint, typecheck, tests, and production build;
   - backend and frontend dependency security;
   - secret scanning;
   - release configuration; and
   - `v2-gateway-contract` for schema-2 changes.
2. Run only focused local checks with constrained concurrency. A typical Go
   command is `GOMAXPROCS=2 go test -p 1 <target packages>`.
3. Run the V2 contract scripts serially:

   ```bash
   python3 scripts/saiai-cli/verify-v2-gateway.py
   bash scripts/saiai-cli/test-setup-wrappers.sh
   bash scripts/saiai-cli/test-sync-saiai-cli.sh
   ```

4. Verify that dependency lockfiles, release workflows, embedded resources,
   and the six client asset names have not regressed.
5. Review database migrations and explicitly decide whether application
   rollback also needs database rollback. Never assume it does.

## 2. Publish an immutable server candidate

The Docker workflow is intentionally opt-in. Immediately before merging the
exact release PR, enable it:

```bash
gh variable set ENABLE_PUBLIC_IMAGE_PUBLISH \
  --repo yuns2023/saiai-server \
  --body true
```

Merge once, then monitor the Docker Publish run for that exact `main` SHA. The
workflow must pass its test gate before building and must verify the pushed
image's source label, executable, and model-pricing resource.

Record the output digest and disable automatic publishing immediately:

```bash
gh variable set ENABLE_PUBLIC_IMAGE_PUBLISH \
  --repo yuns2023/saiai-server \
  --body false
```

Disable the variable even when publishing or verification fails, before
investigating the failure. Do not leave automatic publishing enabled between
release attempts.

Confirm that the package is anonymously readable without relying on cached
registry credentials:

```bash
tmp_docker_config="$(mktemp -d)"
trap 'rm -rf "$tmp_docker_config"' EXIT
DOCKER_CONFIG="$tmp_docker_config" \
  docker manifest inspect "$SERVER_IMAGE" >/dev/null
```

On the target host, pull and inspect the digest before maintenance:

```bash
docker pull "$SERVER_IMAGE"
test "$(docker image inspect --format '{{.Architecture}}' "$SERVER_IMAGE")" = amd64
test "$(docker image inspect \
  --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' \
  "$SERVER_IMAGE")" = "$SERVER_SOURCE"
docker run --rm --network none --entrypoint /app/sub2api \
  "$SERVER_IMAGE" --version
docker run --rm --network none --entrypoint /bin/sh "$SERVER_IMAGE" \
  -c 'test -s /app/resources/model-pricing/model_prices_and_context_window.json'
```

Use the tagged GitHub Release workflow only when a server release archive is
also required. `ENABLE_PUBLIC_RELEASE`, the protected `release` environment,
immutable Releases, and protected `v*` tags are separate from the Docker-only
path.

## 3. Stage the client bundle

`stage` is the only networked client operation. Run it before maintenance with
an explicit tag and independently recorded manifest hash:

```bash
test "$(sha256sum "$CLIENT_SYNC_SCRIPT" | awk '{print $1}')" \
  = "$CLIENT_SYNC_SCRIPT_SHA256"
SAIAI_CLIENT_DIR="$CLIENT_DIR" \
  "$CLIENT_SYNC_SCRIPT" stage \
  "$CLIENT_TAG" "$CLIENT_MANIFEST_SHA256"
```

Verify the staged file set, all six binary hashes and sizes, all three wrapper
files, and the native binary version. Do not activate `latest` or a tag with a
different manifest hash.

## 4. Quiesce traffic and back up

Freeze any automatic failover or DNS-changing automation before enabling
maintenance. The ingress maintenance rule must affect only the Gateway domains;
unrelated virtual hosts and services must retain their previous status.

After maintenance is externally visible:

1. drain active Gateway connections;
2. stop only the application service if a consistent data-directory snapshot
   requires it;
3. back up the active Compose file;
4. copy or hash the running server binary;
5. archive the currently public client directory;
6. archive application data;
7. create a PostgreSQL custom-format dump; and
8. write SHA-256 plus mode/owner/size manifests.

Validate, do not merely create, the backups:

```bash
sha256sum -c "$BACKUP_HASH_MANIFEST"
tar -tzf "$CLIENT_BACKUP_TAR_GZ" >/dev/null
tar -tzf "$DATA_BACKUP_TAR_GZ" >/dev/null
pg_restore --list "$POSTGRES_BACKUP" >/dev/null
```

Backups and release records can contain operationally sensitive metadata. Use
private directories and restrictive modes.

## 5. Activate the pair

Activate the already staged client bundle offline:

```bash
test "$(sha256sum "$CLIENT_SYNC_SCRIPT" | awk '{print $1}')" \
  = "$CLIENT_SYNC_SCRIPT_SHA256"
SAIAI_CLIENT_DIR="$CLIENT_DIR" \
  "$CLIENT_SYNC_SCRIPT" activate \
  "$CLIENT_TAG" "$CLIENT_MANIFEST_SHA256"
```

Verify the active symlink target and manifest hash. The host mount source and
container mount destination must use the same absolute runtime path because
the active symlink points into the immutable release tree.

Generate a candidate Compose file and assert its exact semantic diff. During
the initial schema-1/legacy-client to schema-2/V2 transition, the expected
changes are normally limited to:

- the server image digest;
- the client runtime mount; and
- `SAIAI_CLIENT_DIR` replacing the legacy client directory variable.

For later schema-2 to schema-2 releases, the Compose change is normally only
the server image digest; the client changes through the separately verified
active symlink. Record and review both even when the Compose diff is one line.

Validate the candidate with `docker compose config -q`, replace the active file
atomically, and recreate only the application service:

```bash
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" config -q
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" \
  up -d --no-deps --force-recreate --pull never sub2api
```

## 6. Verify locally before reopening traffic

The application must be healthy with restart count zero. Verify the configured
image, read-only client mount, `SAIAI_CLIENT_DIR`, server binary hash, active
client manifest, and the recorded `EXPECTED_MIGRATION`.

Then verify the local HTTP boundary:

- `/health` returns 200;
- unauthenticated `/api/v1/client/bootstrap` returns 401 with
  `Cache-Control: no-store` and `Vary: Authorization`;
- authenticated bootstrap returns `data.schema_version == 2`;
- `/saiai-cli/manifest.json` matches the recorded hash;
- all six binary responses match the manifest size and SHA-256;
- all three wrappers are non-cacheable and render the public request origin;
  and
- the embedded SPA returns HTML.

Bootstrap is the deployment canary because it is local and non-billable. Read a
canary key without echo and pass it through curl configuration input so it is
not placed in process arguments:

```bash
read -r -s -p 'SAIAI canary API key: ' SAIAI_CANARY_KEY
printf '\n'
bootstrap_body="$(mktemp)"
trap 'rm -f "$bootstrap_body"; unset SAIAI_CANARY_KEY' EXIT

status="$({
  printf 'header = "Authorization: Bearer %s"\n' "$SAIAI_CANARY_KEY"
} | curl -q --config - -sS \
  -o "$bootstrap_body" -w '%{http_code}' \
  "$GATEWAY_ORIGIN/api/v1/client/bootstrap")"

test "$status" = 200
jq -e '.code == 0 and .data.schema_version == 2' "$bootstrap_body" >/dev/null
```

Never use `/v1/messages` or another live provider request for this smoke test.

## 7. Reopen and verify publicly

Restore the original ingress configuration atomically, validate it, and reload
the proxy. Confirm externally that:

- Gateway health and the SPA return their normal status;
- independent virtual hosts retain their pre-maintenance status;
- the public manifest hash is exact;
- public unauthenticated bootstrap retains its 401/no-store/Vary behavior; and
- public authenticated bootstrap returns schema 2.

Only after those checks pass should failover or DNS automation be resumed.

## 8. Roll back as a pair

If any local or public check fails, return to maintenance first.

For the initial schema-1/legacy-client to schema-2/V2 transition, restore the
previous Compose file so its previous image, legacy client mount, and legacy
environment variable return together. An activated V2 symlink may remain on
disk because the old Compose file does not mount it; do not delete a verified
bundle merely to complete this rollback. Restore `PREVIOUS_COMPOSE_FILE` at
`COMPOSE_FILE` atomically with its original owner and mode, validate it with the
explicit project directory, and recreate only the application service.

```bash
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" config -q
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" \
  up -d --no-deps --force-recreate --pull never sub2api
```

For a later schema-2 to schema-2 release, restoring Compose alone is
insufficient because both versions normally mount the same active client
symlink. Re-activate the previous client bundle offline with its retained
script and independently recorded coordinates, and restore the previous server
image digest while maintenance remains enabled. Atomically restore the retained
`PREVIOUS_COMPOSE_FILE` at `COMPOSE_FILE` before running:

```bash
test "$(sha256sum "$PREVIOUS_CLIENT_SYNC_SCRIPT" | awk '{print $1}')" \
  = "$PREVIOUS_CLIENT_SYNC_SCRIPT_SHA256"
SAIAI_CLIENT_DIR="$CLIENT_DIR" \
  "$PREVIOUS_CLIENT_SYNC_SCRIPT" activate \
  "$PREVIOUS_CLIENT_TAG" "$PREVIOUS_CLIENT_MANIFEST_SHA256"
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" config -q
docker compose --project-directory "$DEPLOY_DIR" -f "$COMPOSE_FILE" \
  up -d --no-deps --force-recreate --pull never sub2api
```

In both cases, verify the previous image, health, active client manifest, and
public assets before reopening traffic.

Do not restore PostgreSQL automatically. If the release added no migration,
application rollback normally leaves the database intact. Use the database
backup only for a confirmed data or migration recovery need, while maintenance
remains enabled.

## 9. Record and review

The private release record should bind:

- release ID and UTC timestamps;
- server source, image digest, binary hash, and workflow run IDs;
- client source, tag, manifest hash, activation-script hash, and active target;
- previous pair coordinates;
- Compose, binary, client, data, and PostgreSQL backup paths and hashes;
- ingress maintenance/restore evidence;
- database migration state;
- local and public validation results; and
- exact rollback instructions.

After the release:

- confirm all `main` workflows succeeded for the deployed SHA;
- confirm dependency alerts are zero or explicitly triaged;
- keep publish variables disabled;
- update public contract/provenance documentation when compatibility changes;
- update private operational memory with site-specific coordinates; and
- classify account credential failures separately from release regressions.

Keep the previous pair until real Windows, Claude, and Codex workflows have
been exercised and the operator explicitly approves retirement.
