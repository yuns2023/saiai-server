# Input moderation results and Jev settings

Group moderation remains opt-in. Only the newest real user text is classified;
requests containing only tool results do not reclassify earlier conversation text.

Usage history exposes `moderation_result` independently of risk events:

- `pending`: submitted for asynchronous processing.
- `passed`: completed below the group's risk threshold; all returned category
  scores and the evaluated threshold are retained.
- `flagged`: at least one category meets the threshold; existing risk-event and
  enforcement rules still apply.
- `skipped`: no new user text, or the group was disabled before processing.
- `failed`: processing failed, the queue was full, or the job expired. Durable
  processing failures remain retryable and a later success replaces failure.

Results contain no prompts, provider responses, credentials, or raw errors.
Correlation includes request ID, source, turn, user, API key, and group. Terminal
passed/flagged results cannot be overwritten by late pending/failure writes.
Successful results never create risk events or strikes. Historical usage without
result metadata remains explicitly unknown, not retroactively marked passed.

Migration 103 adds the result metadata table; apply it before this application
version. User, API key, or group deletion cascades to the associated results.
Rollback does not require dropping the additive table or restoring the database.

## Admin configuration

Settings → Gateway → Jev input moderation uses admin-only `GET/PUT
/api/v1/admin/settings/jev`. The endpoint accepts a complete HTTPS URL without
credentials, query, or fragment. Saving performs no provider request.

PUT fields are `endpoint`, `api_key`, and `clear_key`. An empty API key preserves
the effective key; explicit `clear_key` removes it without falling back to the
environment. Changing the URL requires a replacement key or explicit removal,
so an existing credential is not silently forwarded to a new address. Saved
configuration selects Jev; the model remains pinned to `jev-1.13.0`.

Before the first admin save, existing environment configuration remains effective.
After saving, the database configuration takes precedence for subsequent jobs
across replicas without restarting. Jobs already in flight finish with their
resolved configuration. Database/decryption errors fail moderation instead of
silently using an old key. The response exposes only `key_configured`, never the
key or ciphertext. The database stores the key encrypted with AES-GCM using the
existing stable server encryption key; saving is disabled without that key.

Clearing the key causes subsequent text moderation jobs to fail until a key is
configured again. Group switches and enforcement settings are not changed by
saving provider configuration. This feature does not make moderation synchronous
or block the original model request while classification runs.
