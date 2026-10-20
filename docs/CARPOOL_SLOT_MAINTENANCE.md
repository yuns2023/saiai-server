# Carpool periodic admission budgets

Carpool has one fixed device limit and, when periodic maintenance is enabled,
a separate limit on new device admissions per cycle. With capacity 10, zero
registered devices and a cycle allowance of 2, only 2 devices may register in
that cycle. The remaining physical capacity does not permit extra admissions.
Existing registered devices do not consume another admission when requesting.

The account's `extra` fields are:

- `claude_oauth_carpool_device_limit`: existing fixed capacity, 1–32.
- `claude_oauth_carpool_auto_expand_enabled`: retained wire-compatible boolean;
  now enables periodic admission budgets, not capacity expansion.
- `claude_oauth_carpool_maintenance_interval_hours`: integer hours, default 24,
  bounded to 1–720.
- `claude_oauth_carpool_maintenance_admissions_per_cycle`: new device admissions
  per cycle, default 0, bounded to 0–device limit. Zero means unlimited cycle
  admissions while retaining the fixed device capacity. The legacy
  `claude_oauth_carpool_maintenance_free_slots` is a read-only fallback only when
  the new field is absent; explicit zero overrides it.
- `claude_oauth_carpool_next_maintenance_at`: server-owned UTC RFC3339 timestamp.
- `claude_oauth_carpool_maintenance_generation`: server-owned configuration
  generation used to prevent stale workers from resetting a newer budget.

With a positive allowance, enabling starts a first cycle with the configured
allowance, without evicting existing devices immediately. At each due time, atomically reset the allowance
to the configured number and remove the least-recently-seen records only if
needed to make room for that many admissions. For capacity 10 and allowance 2,
counts 10, 9, 8 and 0 cause 2, 1, 0 and 0 removals, but each new cycle permits
at most 2 new registrations. Unused allowances never accumulate. Device
registration and budget consumption share one Redis transaction, including
cross-replica concurrency and repeat requests from the same device.

Manual deletion does not refund spent admissions. An evicted device may
reapply, consuming one admission just like another unregistered device.
Existing streams are not actively terminated. User device quotas and other
OAuth modes are independent. With maintenance disabled, normal fixed-capacity
admission applies; unlimited mode bypasses the local device registry.

Each account has an independent schedule. Enabling or changing the interval
starts a new cycle and schedules the next adjustment one full interval from
save time. Switching to zero bypasses any spent cycle budget immediately, and
periodic adjustments do not evict devices to reserve capacity. Changing a
positive admission count applies immediately, retaining consumed admissions
and the reset time. Decreasing below consumed admissions leaves zero
remaining and does not evict or reject already registered devices. Switching
back from zero uses the retained budget usage (unlimited admissions do not spend
that budget). Changing capacity retains the schedule and applies the existing capacity gate.
Disabling and re-enabling starts a new generation. The server ignores submitted
schedule/generation values. Bulk edits normalize each account independently.

The worker checks once per minute, so adjustment may lag the displayed time by
about a minute plus processing time. Missed periods result in only one reset,
retaining the original schedule phase. Redis records the due-time token with
the reset/removals, so retries cannot restore spent allowance or evict twice.
PostgreSQL conditionally advances the due time without overwriting a newer
schedule. Failure to advance PostgreSQL after a successful Redis transaction
is retryable. The registry and budget depend on Redis persistence; loss of
Redis state is not a supported deduplication boundary.

The device overview API returns `maintenance_enabled`, `admissions_remaining`
(-1 for unlimited cycle admissions; otherwise the smaller of the remaining
cycle allowance and physical space), and
`next_maintenance_at`. The device view shows both remaining admissions and the
next adjustment in the browser's local timezone; Refresh reloads these values.
The edit form also shows the next adjustment or indicates that saving a new
interval will start a new schedule.

Existing enabled accounts without a schedule are initialized on the first
worker pass and wait a full interval before the first reset/eviction. The actual capacity is preserved; absent allowance fields mean 24 hours and
unlimited cycle admissions. Existing explicitly positive or legacy allowances
retain their limits. The old target/calendar-day fields are ignored. No SQL schema
migration is required. A rollback to the previous budget implementation interprets zero as a fallback
allowance (normally 1), so unlimited-cycle behavior requires this Gateway version.
`single_device` retains its existing midnight expansion/rotation behavior.
