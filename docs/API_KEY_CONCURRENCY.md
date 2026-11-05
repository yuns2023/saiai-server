# Per-API-key concurrency

Every API key has its own concurrency cap next to the user's. It was added
after a team user's single Claude Code session fanned out more requests than
anyone else on the account could use (Ops error #224957, 2026-11-05). The cap
limits how much of the user's concurrency one key can take. It does not stop
an account's wait queue from filling: a sticky Claude session that outgrows
its account still gets `Too many pending requests` (see
`CLAUDE_SESSION_AFFINITY.md`).

## Setting (api_keys, migration 120)

| Column | Default | Meaning |
| --- | --- | --- |
| `max_concurrency` | `5` | Concurrent requests of this key, `1..1000` (CHECK `api_keys_max_concurrency_range`). Existing keys got 5 too. |

User API (`POST /api/v1/keys`, `PUT /api/v1/keys/:id`): `max_concurrency`
absent keeps the default on create and the stored value on update; values
outside `1..1000` are a 400. It is **not** capped by the user's concurrency:
caps of all keys may add up to more than the user's total, and whoever takes
a slot first gets it. The admin key editor still changes only the group. The
field is in the API key auth cache snapshot (schema v6).

## Runtime

- Each request holds one user slot and one key slot under the same request
  ID. `ConcurrencyService.AcquireUserAPIKeySlot` takes both in one Redis
  script (`concurrency:user:<id>`, `concurrency:apikey:<id>`): both or
  neither, so a request never waits for the user while holding a key slot.
  A user with concurrency `<= 0` stays unlimited and only the key slot is
  taken. A key limit `<= 0` in the request context falls back to 5, never to
  unlimited. A retried acquire with the same request ID finds its own slot.
- HTTP paths (Claude Messages, Gemini, Responses, images, ChatGPT
  conversation) wait as before, inside the user's wait queue. A timeout
  returns 429 `Concurrency limit exceeded for api key` when the key was the
  limit, `for user` otherwise.
- Responses WebSocket stays try-only: a full key closes with
  `too many concurrent requests for this API key`. Later turns and failovers
  re-read the key just before taking slots, so a changed cap applies to the
  next admission. Overlapping turns on one connection still share one slot
  pair, as for user slots.
- Not counted: `count_tokens`, model listing and control requests, as for
  user slots. Slots have the same TTL and no renewal; startup cleanup also
  clears `concurrency:apikey:*` members of other process prefixes, which
  assumes one Gateway process per Redis.

## Rollout

Release with a stop-old/start-new switch per site. An old binary does not
take key slots and ignores `max_concurrency` on key updates. Before the
release, tell the owners of keys whose recent peak concurrency is above 5;
they will be limited to 5 until they raise it.
