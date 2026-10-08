# Native Chat background delivery (experimental)

This candidate implements a scoped delivery broker for the native ChatGPT
protocol observed in Desktop 26.1002.6548.0. It requires both
`gateway.openai_chat_enabled` and `gateway.openai_chat_updates_enabled`.
Both default to false. Local protocol mocks do not establish acceptance on the
installed official Desktop or a provider plan. Activate only a compatible
Server/Client preview pair with separate recorded Desktop acceptance.

## Authentication and ownership

`GET /chatgpt/backend-api/celsius/ws/user` returns a credential-free
`websocket_url` pointing at the managed local-proxy host and the
`/backend-api/saiai/chat-updates` path. The local proxy maps this to the
authenticated Gateway WebSocket endpoint and substitutes the SAIAI Key.
Bootstrap neither selects an OAuth account nor contacts the provider. An old
Gateway returns an unsupported response; a compatible Client is required.

Each subscription is scoped by authenticated user, API Key and group. A
successful native response establishes a hashed conversation-to-turn binding
before that chunk is delivered. The same binding fixes the selected OAuth
account. Account discovery contains only IDs of accounts already used by that
scope, never arbitrary pool members. Redis is mandatory outside replay/simple
mode. All related keys share a scope hash tag and retain the original one-hour
turn expiry. Raw conversation IDs, topic IDs, provider subscription URLs,
credentials, message content and image pointers are not persisted.

The broker multiplexes at most 16 accounts per scope, 32 topics per connection,
two connections per scope and 64 per process. It checks group/account
eligibility during delivery. Expired ownership, unavailable owners and Redis
errors fail closed. It does not fail over an owned conversation to another
account. This bounded lease does not implement permanent conversation history.

The provider's account-wide subscription URL remains in server memory. It is
never returned to Desktop or passed through the Responses trace logger. Only
WSS destinations on the approved ChatGPT/Azure PubSub host suffixes are allowed;
an explicit replay override additionally permits its exact origin. Redirects,
userinfo, fragments and arbitrary ports/hosts are rejected. The actual
provider destination still needs versioned staging verification.

## Native protocol boundaries

The Gateway accepts native connect, presence, subscribe and unsubscribe
commands. The logical `conversations` subscription is scoped locally. Turn
topics must have been observed in a successful owned stream handoff. Provider
command replies are consumed internally; catchup messages undergo the same
ownership filtering as live messages. Unknown account-wide events are dropped.

Allowed conversation events must identify an owned conversation on that same
account. Turn stream messages must match their owned topic and, when supplied,
the bound conversation. Encoded stream items remain opaque and unchanged.
Known handoff topics and generated image assets are bound before delivery.
Single-conversation GETs and image downloads require the same ownership and
original account; they do not select an arbitrary pool account.

Intentional delivery transformations are the local subscription URL, logical
command acknowledgments, wrapping direct conversation events in native topic
messages, filtering other users' events/catchups, and omission of global topic
offsets. Multiplexed accounts do not share a recovery cursor, so global
subscriptions advertise `recovered=false` and use filtered provider history.
Turn-topic offsets and native payloads are preserved. This is a delivery broker,
not a byte-identical account-wide subscription claim. A local subscription ack
does not prove the provider connection is established.

Frames are bounded (64 KiB client commands, 2 MiB provider frames), queues have
bounded backpressure, writes/timeouts are bounded, and the connection lifetime
is at most one hour. Disconnect closes its provider connections and releases
broker reservations. Provider failures close the downstream transport so the
official client can reconnect. The broker sends no generation commands,
replays no model request, and holds no model-concurrency slot between delivery
legs. Metadata-only connection logs contain account/Key IDs, never frames or
credentialed URLs.

## Async completion and accounting

`message_stream_complete` ends one delivery stream. It cannot settle a turn
while observed async status remains STREAMING (3), REALTIME (5/6/7), malformed
or unknown. Pending state is shared across resumes, including resumes that omit
status. The Redis seal checks pending state atomically with image evidence;
late delivery cannot change an already sealed bill.

A native completion hint may trigger a read-only GET of the owned conversation
on its original account, at most three times per conversation per connection.
This is not another model request. Gateway-owned
delivery reads request identity encoding and remove WebSocket negotiation
headers. The JSON must prove an inactive native state (1/2/4), a successful
terminal assistant message with `end_turn=true`, and a current branch descending
from the original user's message hash. Old branches, incomplete tasks, missing
schemas, errors and mismatched conversations cannot settle. Only generated
images on this new branch count; historical images do not count again. Replayed
message updates bind download ownership without adding images to a bill.

HTTP, resume and background delivery settle the original message identity,
snapshotted model/effort, Chat/image tariffs and subscription identity through
one atomic debit namespace. The usage row remains attributed to the original
native generation endpoint. Subscription expiry/change during delivery cannot
silently switch an admitted subscription turn to the wallet. Delivery reads
remain available after balance/quota exhaustion; disabled/expired credentials,
inactive users and IP restrictions remain enforced. New generation still
requires normal billing eligibility.

The tests cover async stream termination, omitted resume status, current-branch
image accounting, repeated notifications, subscription identity, scope/account
filtering, catchups, turn topics, download ownership and depleted-credit
delivery. They use local mock traffic only. Native private-protocol variants,
actual PubSub destinations, reconnect recovery and all official Desktop
platforms still require recorded acceptance of the exact preview artifacts.

No database migration or tariff change is part of this candidate. Rollback must
disable the updates feature before restoring an older Server/Client pair.
