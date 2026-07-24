# User API Key usage analytics

The user dashboard treats an API Key as a general reporting dimension. It does
not create or expose employee, member, or cost-center entities. A customer can
choose any naming convention for Key names.

## Dashboard filters

These authenticated user endpoints accept an optional owned `api_key_id`:

- `GET /api/v1/usage/dashboard/stats`
- `GET /api/v1/usage/dashboard/trend`
- `GET /api/v1/usage/dashboard/models`

The server verifies Key ownership before running an aggregate query. A selected
Key filters the summary, trend, and model data. The dashboard also applies the
same Key and date range to its recent-usage request.

## Key ranking

`GET /api/v1/usage/dashboard/api-key-breakdown` returns the current user's
non-deleted Key inventory, including zero-usage Keys, with usage aggregated for
the requested range.

Query parameters:

- `start_date` and `end_date`: inclusive calendar dates in `YYYY-MM-DD`;
- `timezone`: IANA timezone supplied by the Web client;
- `page` and `page_size`: pagination, with at most 100 rows per page; and
- `sort`: `actual_cost_desc`, `requests_desc`, `tokens_desc`,
  `last_used_desc`, or `name_asc`.

The response contains Key ID, Key name, status, last-used time, request count,
the four token classes, total tokens, standard cost, actual cost, and actual
cost share. Its summary covers all matching Keys, not only the current page.
The raw API Key value is never selected or returned.

The Web UI supports ranking by cost, requests, tokens, last use, or Key name;
row-level filtering; deep links into detailed usage; and a CSV export.
Historical availability follows the deployment's usage-log retention policy;
this first version does not add a separate long-term per-Key aggregate table.

The older batch Key-usage endpoint retains its compatibility fields. Its
`total_actual_cost` default window is the last 30 days, and the Key-management
UI labels it as such rather than as lifetime usage.

## OpenAI service-tier billing

Gateway forwarding and local billing have separate boundaries. Preserve the
request and upstream response shape, but do not let a downstream Gateway's
reported tier reduce the local charge below an explicit request tier.

Normalize the supported tiers as `priority`, `default`, and `flex`, ordered
from most to least expensive. When both request and response tiers are known,
the effective billing tier is the more expensive one:

- an explicit `priority` request remains `priority` even if the downstream
  response reports `default`;
- a response may raise the effective tier above the request tier; and
- an absent or unknown value does not invent a tier.

Apply the same resolver to HTTP, WebSocket, and WebSocket v2 usage recording.
Store the effective tier with the usage record while returning the original
upstream response unchanged.

User and administrator usage tables show the effective billing tier as
`Fast`, `Standard`, or `Flex` beside the charged amount. Historical records are
not recalculated merely because this resolver changes; any backfill is a
separate, explicitly reviewed billing operation.
