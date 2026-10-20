# Claude session affinity under concurrency pressure

Claude OAuth/setup-token sticky scheduling and Claude Code Anthropic scheduling
keep an eligible bound account when its concurrency slots are occupied. This
applies both with and without model routing and load batching. A full account
waiting queue does not authorize spilling the session onto another account.
The handler enforces the configured waiting limit and timeout; a full queue
returns the existing retryable rate-limit error. Waiting does not serialize the
whole session: requests can run concurrently up to the account's configured
limit.

Official Claude Code requests with a session key also reserve an account and a
unique ownership generation in Redis. Selection claims that generation with
compare-and-set before forwarding. Conflicting selections release their slot
and retry against the winner. Redis reservation errors stop selection instead
of silently sending the session to another account. A request's early sticky
prefetch is refreshed from this ownership record.

Ownership is distinct from success confirmation. Initial reservations expire
after two minutes; an active request renews its generation while waiting or
streaming. Success creates the confirmed sticky with the existing maximum
one-hour TTL. A failed side request does not erase the owner or another
request's confirmation. Account health and the existing bounded upstream retry
policy determine whether the next selection can migrate. Model routing,
model eligibility, group membership and quota checks continue to apply.

Migration atomically changes the generation and pending account. Only success
from the current generation can confirm the binding. Late success from an old
account, including an A-to-B-to-A sequence, cannot overwrite the new generation.
Already dispatched requests can finish; their results are not canceled merely
because another request caused a migration. Requests still waiting when the
generation changes return a retryable error before forwarding on the old
account. Expired generations cannot be
revived by a late result or renewal.

Pinned device routing keeps its existing separate contract. Non-Claude clients
retain their existing scheduling policy. This change does not alter billing,
client request bodies, account concurrency limits or the waiting configuration.

Validation covers full queues with an idle alternative, model routing and legacy
scheduling, stale prefetches, concurrent reservation winners, cache failures,
late completion and Redis generation/TTL isolation. Provider traffic is not
needed for validation.
