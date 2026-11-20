# Claude Code device information

The admin user's Claude Code device list returns `client_hints` with the last
non-empty inbound `user_agent`, `os`, `arch`, `runtime`, `runtime_version`, and
`sdk_version` values. These come from User-Agent and the corresponding
X-Stainless headers before account-specific rewriting. Missing headers preserve
previously observed values. They are client-reported display information, not
verified hardware identifiers, and do not affect device identity or admission.

Migration 099 adds a JSONB column to the existing SQL-managed device table;
there is no Ent schema for this table. Historical rows start with empty hints
and are populated when the device is next seen. Reactivation preserves captured
fields and merges newly reported values. The admin UI shows missing values as
an em dash.

Bounded carpool recorded and overflow devices return `last_owner`, an
authenticated request snapshot containing user ID, username, email, Key ID and
Key name. The UI labels it “Last user” / “Last Key”: a device can use different
Keys or users, and the latest known identity replaces the entire snapshot.
Names describe the request-time identity; later renames are reflected on a
subsequent request after authentication cache refresh. Raw Keys and authorization
headers are never copied into these display records. Authentication cache
snapshots preserve Key name and user email so cached authentication retains the
same display information as uncached authentication.

Existing Redis records remain readable without an owner. Their ownership is
populated on a subsequent authenticated request. Unlimited carpool retains its
existing behavior of not collecting new device registry entries.

## Device platform observation (observe-only, Server #243)

Purpose: surface a Claude Code device id (`metadata.user_id.device_id`, a random
64-hex value stored in the client's `~/.claude.json` and therefore copyable)
that shows up on different platforms or under different SAIAI users. It never
blocks, alerts, rewrites, or delays a request; queue-full, cap and Redis errors
only increment per-process counters (returned as `stats` by the admin API).

Where: every Anthropic OAuth / setup-token request in `prepareOAuthRequestIdentity`
(all four modes, Messages and count_tokens) after the original user id parses,
plus the count_tokens auxiliary early return when a valid device id is present.
Observed once per inbound request (failover does not double count). Admin
account tests, scheduled tests and the models catalog are out of scope.

Platform sample:
- family + arch from `X-Stainless-Os` / `X-Stainless-Arch` (every request);
  `Platform:` in the system prompt cross-checks the family, and a conflict
  makes that dimension unknown.
- major version from the `# Environment` section's `OS Version:` line, present
  only on main-conversation requests (not subagent, background Haiku or
  count_tokens). Captured shapes (2026-11-20): `Darwin 24.3.0` (macOS 15.3.1),
  `Windows 11 Pro 10.0.26200`, `Linux 7.0.0-43-generic`.
  - macOS: Darwin 19→10.15, 20–24→11–15, 25→26; other values unknown.
  - Windows: NT build from `10.0.<build>`, ≥22000→11, 10240–21999→10; the
    product-name text is ignored (older runtimes report Win11 as "Windows 10").
  - Linux: no major (the line is the kernel, not the distribution version).
- System text scan is bounded (16 blocks, 128 KiB total) and blocks are not
  concatenated. Owner = authenticated API key; only user id and key id are
  stored, usernames and emails are resolved when an admin reads the list.

Reasons (all listed; the UI shows the first three by default):
`cross_family` (two known families within 24 h), `version_alternation`
(an older major reappears after a newer one; same-millisecond ties count),
`cross_user` (≥2 user ids in 30 days), `arch_only` (same family, several
arches — Rosetta / ARM emulation), `sequential_upgrade` (old major's last seen
before the new one's first seen). Unknown values never create a difference.
Docker mounting the same `~/.claude`, Rosetta and OS upgrades are legitimate
causes.

Storage (per site Redis, prefix `claude_device_platform_v1:`; one Lua script per
observation, Redis `TIME` for pruning, caps, the daily-counter day and TTLs):
- `dev:<sha256>` hash: display prefix (first 8 hex), per family|arch and
  family|major first/last (ms event time, min/max merge), up to 20 users with
  `ut` truncation flag, per-reason last_matched. TTL 30 days.
- `tracked` / `flagged` zsets with key TTL 31 days refreshed per write.
- Caps: 20,000 tracked devices per site, ≤50 new devices per user per UTC day,
  requests without an API key owner cannot add new devices. When a cap is hit
  only new devices are dropped; existing ones keep updating.
- Measured size: ~250 B (one user) to ~2 KB (max platforms, 20 users) per device.
- Retention promise: the list shows devices flagged in the last 30 days; every
  key expires. After a site stops observing entirely, index members can linger
  up to ~61 days (bounded by the cap).
- Local dedupe (5 min, per process) applies only to samples without a known
  major, so alternations seen through different replicas are not lost; the
  request path uses `TryLock` and never waits.

Admin: `GET /api/v1/admin/claude-device-platform/flagged?limit=&cursor=&reasons=`
(limit ≤100, cursor paging over a live view), UI `/admin/claude-devices`.
Data is per site; the three sites do not share it. Repository Redis tests run
only with `SAIAI_TEST_CARPOOL_REDIS_SOCKET` set (CI skips them, like the carpool
tests).
