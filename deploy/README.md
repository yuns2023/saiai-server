# SAIAI Server deployment templates

This directory contains generic self-hosting examples. It contains no
production credentials or site-specific topology. Review every value before
using a template on an internet-facing host.

## Local source build

Use the source-build Compose file for local development or a deliberate custom
source build:

```bash
git clone https://github.com/yuns2023/saiai-server.git
cd saiai-server
cp deploy/.env.example deploy/.env
```

Set at least unique values for:

- `POSTGRES_PASSWORD`
- `ADMIN_EMAIL`
- `ADMIN_PASSWORD`
- `JWT_SECRET`
- `TOTP_ENCRYPTION_KEY`

Generate secrets with a cryptographically secure tool such as:

```bash
openssl rand -hex 32
```

Then build and start the stack:

```bash
docker compose -f deploy/docker-compose.dev.yml up -d --build
docker compose -f deploy/docker-compose.dev.yml logs -f sub2api
```

The service listens on `${SERVER_PORT:-8080}`. Put it behind an HTTPS reverse
proxy before exposing it to untrusted networks.

## Published image templates

`docker-compose.yml` uses named volumes. `docker-compose.local.yml` uses local
directories. `docker-compose.standalone.yml` expects external PostgreSQL and
Redis services.

All three accept an exact image reference through `SAIAI_SERVER_IMAGE`:

```bash
SAIAI_SERVER_IMAGE='ghcr.io/yuns2023/saiai-server@sha256:<digest>' \
  docker compose -f deploy/docker-compose.local.yml up -d
```

Pin a digest for real deployments. The default `:main` image is a development
convenience and exists only after the repository owner explicitly enables the
public image workflow.

## SAIAI V2 client assets

Gateway schema 2 and the matching `saiai-client` release are one compatibility
pair. Stage and verify the client release before enabling its public asset
routes. Staging is safe to do before maintenance:

```bash
sudo install -d -m 0755 /var/lib/saiai-server/client-runtime
CLIENT_TAG='saiai-v0.9.0'
CLIENT_MANIFEST_SHA256='092107c40b60cf0174e7278891fbb3cb097ccbe7cc05e8bef05e411687dfa02a'
sudo env SAIAI_CLIENT_DIR=/var/lib/saiai-server/client-runtime/saiai-cli \
  scripts/sync-saiai-cli.sh stage "$CLIENT_TAG" "$CLIENT_MANIFEST_SHA256"
```

Do not run `activate` yet. Activate the staged bundle only inside the same
maintenance window or atomic front-door switch that changes the Gateway image
to its matching schema-2 digest:

```bash
sudo env SAIAI_CLIENT_DIR=/var/lib/saiai-server/client-runtime/saiai-cli \
  scripts/sync-saiai-cli.sh activate "$CLIENT_TAG" "$CLIENT_MANIFEST_SHA256"
```

Verify both the Gateway and public client assets before reopening traffic.
Rollback must restore the previous Gateway and client bundle together.

The host runtime parent and the container mount target must use the same
absolute path because the active symlink points to an immutable release bundle:

```yaml
services:
  sub2api:
    volumes:
      - /var/lib/saiai-server/client-runtime:/var/lib/saiai-server/client-runtime:ro
    environment:
      - SAIAI_CLIENT_DIR=/var/lib/saiai-server/client-runtime/saiai-cli
```

Without a configured bundle, binary asset endpoints intentionally return 503;
the Gateway itself and its embedded administration UI can still start.

## Configuration

Most settings are available as environment variables in `.env.example`.
`config.example.yaml` documents the full structured configuration. Runtime
copies such as `.env` and `config.yaml` are intentionally ignored by Git.

For an outbound TLS inspection or private CA requirement, mount a local CA
file and set `EXTRA_CA_CERT_FILE` explicitly. Never commit a CA private key.

## Upgrade and rollback

Copy [`release-pair.env.example`](release-pair.env.example) into a private
operations repository and record the Gateway source SHA, image digest, client
source/tag, manifest hash, and previous pair. Do not put credentials in that
file.

The required sequence is prepare and stage, enter maintenance, validate
backups, activate the pair, verify locally, reopen traffic, and verify publicly.
If a check fails, return to maintenance and restore the previous pair; do not
leave a half-updated Gateway/client combination online. Recreate only the
application service, and do not restore PostgreSQL automatically unless a
confirmed migration or data recovery requires it.

Follow the complete [release operations runbook](../docs/RELEASE_OPERATIONS.md)
for immutable-image checks, backup validation, non-billable bootstrap smoke
tests, public asset verification, rollback, and release records.

## Security reminders

- Do not expose PostgreSQL or Redis publicly.
- Use HTTPS and restrict administrative routes.
- Store credentials outside source control and rotate any exposed value.
- Do not enable full request-body tracing in normal operation.
- Treat backups, logs, client bundles, and deployment records as sensitive.
- Use provider accounts only with authorization and in accordance with their
  terms.

The helper scripts in this directory are convenience tools, not a substitute
for reviewing the deployment on your own infrastructure.
