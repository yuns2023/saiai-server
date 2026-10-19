# Carpool periodic free-slot maintenance

Carpool has one fixed device limit. Automatic maintenance no longer increases
that limit and no longer uses `claude_oauth_carpool_auto_maintenance_target`.
This policy is independent of user device quotas and other OAuth modes.

The account's `extra` fields are:

- `claude_oauth_carpool_device_limit`: existing fixed capacity, 1–32.
- `claude_oauth_carpool_auto_expand_enabled`: retained wire-compatible boolean;
  now enables periodic free-slot maintenance, not expansion.
- `claude_oauth_carpool_maintenance_interval_hours`: integer hours, default 24,
  bounded to 1–720.
- `claude_oauth_carpool_maintenance_free_slots`: desired free slots after a
  maintenance pass, default 1, bounded to 1–device limit.
- `claude_oauth_carpool_next_maintenance_at`: server-owned UTC RFC3339 timestamp.

At each due time, atomically remove the least-recently-seen records until the
record count is at most `limit - free_slots`. Do nothing if enough slots are
already free. For limit 10 and free slots 2, counts 10, 9, 8 and 6 cause 2, 1,
0 and 0 removals. Counts above a newly lowered limit are also reduced to the
same target at the next maintenance pass. The capacity itself never changes.
Old devices may reapply for admission; free slots are not reserved exclusively
for previously unseen devices. Existing streams are not actively terminated.

Each account has an independent schedule. Enabling the feature or changing its
interval starts a full interval from save time. Changes to capacity or desired
free slots retain the schedule. Disabling maintenance, changing modes or
turning on unlimited devices disables maintenance; re-enabling starts afresh.
The UI shows the persisted next time in the browser's local timezone.

The worker checks once per minute, so actual execution may lag the displayed
time by about a minute plus processing time. Missed periods are skipped after
one pass, preserving the original schedule phase. A no-op pass also advances
the schedule. Redis atomically records the due-time token together with the
removals, preventing repeat eviction after retries or concurrent replicas.
PostgreSQL advances the due time conditionally, avoiding overwrite of a newer
schedule. PostgreSQL failure after successful Redis eviction is retryable.
Redis state must retain its existing persistence guarantees; loss of that state
also loses the local device registry and is not a supported deduplication
boundary.

Existing enabled accounts without a timestamp are lazily initialized on the
first worker pass and wait a complete interval before removal. Their actual
capacity is preserved; absent new fields mean 24 hours and 1 free slot. The old
target and calendar-day fields are ignored. No database schema migration is
required. Older Gateway code still interprets the legacy enable flag as daily
expansion/rotation, so application rollback restores that older behavior.

`single_device` retains its existing midnight expansion/rotation behavior.
