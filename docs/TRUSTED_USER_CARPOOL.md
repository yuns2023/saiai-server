# Trusted users and carpool account eligibility

## Contract

Administrators can mark a user as trusted (`is_trusted`, default `false`). An
Anthropic OAuth or setup-token account in `carpool` mode can opt into
`extra.claude_oauth_carpool_trusted_only: true` (default absent/off).

The independent `extra.claude_oauth_carpool_users_only` switch restricts the
account to the administrator-selected `carpool_user_ids`. Both switches default
to off. Enabled conditions combine with AND:

| Trusted-only | Specified-users-only | Additional eligibility requirement |
| --- | --- | --- |
| Off | Off | None |
| On | Off | Trusted user |
| Off | On | User belongs to the selected list |
| On | On | Trusted user AND membership in the selected list |

An enabled empty list denies everyone. Membership comes from the authenticated
SAIAI Key owner, never request metadata. Migration `107_carpool_account_users.sql`
adds the cascading account/user relation; selection, sticky reuse and final
forwarding use the same predicate. See [account scheduling](GATEWAY_ACCOUNT_SCHEDULING.md#carpool-user-access).

Trust is an account eligibility condition, not a new OAuth traffic mode, user
role, priority, or subscription tier. Carpool eligibility does not change
group membership, quotas, device admission/rotation, session limits, request
rewriting, billing, or success-only sticky behavior. It applies to bounded and
unlimited-device carpool alike. Other account modes ignore this setting; the
account editor removes it when changing away from carpool. The separate input
moderation trusted-user bypass is documented in [input moderation](INPUT_MODERATION.md);
it does not bypass these carpool restrictions.

Only administrator user create/update APIs accept the flag. User profile APIs
do not expose or allow editing it. The authenticated SAIAI Key owner's database
flag is carried through the auth snapshot into request context. Provider
metadata, headers, query parameters, and user roles cannot grant trust. Missing
identity or an old snapshot without the flag means untrusted.

## Implementation

1. Migration `099_add_user_trust.sql` adds a non-null boolean with a false
   default, and the Ent schema/generated ORM persist it. Admin create/edit and
   the user list manage/display trust.
2. The auth repository projection and L1/L2 snapshots include the flag. Changing
   it invokes existing per-user invalidation for all of the user's keys.
3. Carpool create/edit exposes independent trusted-only and specified-users-only
   checkboxes, with user selection for the latter.
   Account edits reuse the existing scheduler snapshot refresh and outbox.
4. Both legacy and load-aware selection apply the condition to model routing,
   ordinary candidates, confirmed/pending/prefetched sticky, retry exclusions,
   and fresh-account admission. A final forwarding guard also covers messages
   and token counting. No user-specific mutation of shared scheduler snapshots
   is performed.
5. No eligible account produces the existing no-available-account response.
   Structured selection diagnostics include `trusted_user_required` counts.

## Changes during a session

After a trust/configuration update propagates through the existing cache
invalidation mechanism, subsequent requests recheck eligibility even when a
sticky binding already exists. They can select another eligible account or fail
with no available account. An in-flight request/stream is not interrupted.
Existing sticky entries cannot bypass eligibility; successful replacement
requests use the existing success-only promotion rules. No bulk device removal
or historical sticky deletion is performed.

This uses the existing cache consistency model (including its failure recovery
and TTLs); it is not a transactional revocation barrier across in-flight requests
or cache outages.

## Validation and rollout

Targeted local tests cover the eligibility matrix, OAuth/setup-token scope,
legacy/load-aware and native/mixed scheduling, routing, all sticky sources,
revocation, fallback exhaustion, forwarding guards, snapshot compatibility,
cache invalidation, middleware identity provenance, and UI round trips.

Migration/schema integration tests include the new column. Local unit/UI tests
use mocks and never send provider requests. Deployment is a separate authorized
operation. Apply the additive migration before running the new binary; leave
account restrictions off until all serving instances enforce them. An older
binary ignores the account restriction, so rolling back to one removes this
protection even though the additive database column can remain.

## Historical trusted-only verification (migration 099)

- Targeted Go unit tests passed for trust eligibility (128 scheduling scenarios),
  existing selection/sticky behavior, middleware, user/admin boundaries, auth
  cache invalidation, and admin-only DTO exposure (`GOMAXPROCS=2`, `-p 1`).
- PostgreSQL/Redis test-container integration tests passed for migration
  idempotency/schema, trusted-user persistence, and API-key auth projection.
- Three Vue test files passed (12 tests): user edit, account edit, and trusted-only
  account creation for both OAuth and setup-token.
- `pnpm run typecheck` passed. `pnpm run lint:check` passed with six pre-existing
  unused-variable warnings in unchanged files. `git diff --check` passed.
- Those original local checks did not establish full CI or deployment. Current
  delivery status comes from private Ops observations, not this historical list.
  The combined access predicate has subsequent unit/UI and PostgreSQL relation
  integration coverage; release acceptance binds the exact candidate SHA.
