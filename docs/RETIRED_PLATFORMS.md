# Retired platforms

Antigravity and Sora are retired product integrations.

The application no longer exposes their gateway, OAuth, client, generation,
media, quota, or storage-management routes. New groups and accounts cannot use
either platform, and existing accounts are excluded from scheduling, connection
tests, usage refreshes, credential refreshes, and operational retries. Historical
accounts remain visible and deletable so operators can inspect old billing and
usage records.

The platform string values, existing database columns, Ent fields, migrations,
pricing fields, and historical response fields are intentionally retained. They
are data-compatibility artifacts, not supported runtime capabilities. Removing
them requires a separate data-retention review, backup plan, and schema migration.

Legacy configuration and setting keys may still be accepted or returned for
backward-compatible decoding, but they do not activate either retired platform.
Do not add new behavior behind those keys.

Route retirement is intentional: dedicated retired-platform endpoints return
`404`, while a request made through a generic gateway route with a legacy group
returns a retired-platform error before account selection or upstream traffic.
