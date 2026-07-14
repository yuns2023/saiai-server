# SAIAI Server container image

Public images, when enabled, are published from protected source revisions to:

```text
ghcr.io/yuns2023/saiai-server
```

Prefer an immutable digest:

```bash
docker pull ghcr.io/yuns2023/saiai-server@sha256:<digest>
```

The image contains the `sub2api` runtime binary for database compatibility and
listens on port 8080 by default. It expects PostgreSQL and Redis. See
[README.md](README.md) for source builds, Compose examples, required secrets,
V2 client-bundle mounting, upgrade, and rollback guidance.

This image is an independent SAIAI distribution derived from Sub2API; it is not
an official Sub2API or upstream-provider image.
