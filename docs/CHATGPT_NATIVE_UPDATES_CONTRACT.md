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

Browser WebSocket handshakes may omit the custom device headers sent with
bootstrap. When bootstrap has a device identity, its managed URL carries a
`saiai_device` query value containing a user/Key/group-scoped digest. Header and
query identities must agree when both are present. This digest groups capacity
only: it is not authentication, conversation ownership or provider attestation.
The original device ID is not exposed, and the query does not reach the
provider. Existing local-proxy query preservation requires no Client update.

Each subscription is scoped by authenticated user, API Key and group. A
successful native response establishes a hashed conversation-to-turn binding
before that chunk is delivered. The same binding fixes the selected OAuth
account. Account discovery contains only IDs of accounts already used by that
scope, never arbitrary pool members. Redis is mandatory outside replay/simple
mode. Active turn, replay, topic and subscription keys share a scope hash tag
and retain the original one-hour turn expiry. Raw conversation IDs, topic IDs, provider subscription URLs,
credentials, message content and image pointers are not persisted.

The broker multiplexes at most 16 accounts per scope, 32 topics per connection,
eight connections per device by default within the user/Key/group scope and 64 per process.
The device ID is digested and never changes subscription ownership. Requests
without a device retain the same configured per-scope legacy limit. Official Desktop opens distinct
conversation, messaging and app-notification transports. The original four-slot
estimate left only one reconnect reservation; eight leaves more room for
multiple windows and overlapping reconnects. Unsupported auxiliary topics remain local and open no
provider connection. It checks group/account
eligibility during delivery. Expired ownership, unavailable owners and Redis
errors fail closed. It does not fail over an owned conversation to another
account.

These notification reservations are independent of account model concurrency
and have no per-connection billing. Opening them does not use one of the
account's model-request slots. Connection diagnostics distinguish bootstrap
device hints, device headers and the legacy shared scope.

Administrators can tune `gateway.openai_chat_updates_connections_per_device`
or `GATEWAY_OPENAI_CHAT_UPDATES_CONNECTIONS_PER_DEVICE`, for example to 16.
The accepted range is 1–64; zero selects the default eight. Negative values or
values above the unchanged global 64-connection bound fail configuration
validation. The effective limit applies to both identified devices and the
legacy unidentified scope; it never changes account model concurrency or
turn/image pricing. Configuration changes require a Gateway restart.

Historical conversation reads, known image downloads and a new generation in
an owned conversation use a separate 30-day routing lease. Activity on a known
conversation renews that lease without extending or recreating its bill. The
lease stores only scoped conversation/asset digests, the original account ID,
and the latest user-message digest and turn start time. An older delayed turn
cannot replace a newer branch identity; another account cannot replace the
owner. This is a bounded delivery lease, not permanent conversation storage.
A still-live legacy turn lazily establishes the new lease. Already-expired
legacy context cannot be recovered by guessing an account: start a new
conversation. The same isolation and current account/group checks apply after
accounting expiry. History and downloads cannot create a successful usage row.

The provider's account-wide subscription URL remains in server memory. It is
never returned to Desktop or passed through the Responses trace logger. Only
WSS destinations on the approved ChatGPT/Azure PubSub host suffixes are allowed;
an explicit replay override additionally permits its exact origin. Redirects,
userinfo, fragments and arbitrary ports/hosts are rejected. The actual
provider destination still needs versioned staging verification.

## Native protocol boundaries

The Gateway accepts native connect, presence, subscribe and unsubscribe
commands. The logical `conversations` and `alder-conversations` subscriptions
are scoped locally. Both occur in one official Desktop startup batch. Unsupported
auxiliary or unowned turn subscriptions receive a per-command unavailable reply;
they cannot close an otherwise valid conversation subscription. Turn
topics must have been observed in a successful owned stream handoff. Provider
command replies are consumed internally; catchup messages undergo the same
ownership filtering as live messages. Unknown account-wide events are dropped.

Allowed conversation events must identify an owned conversation on that same
account. Turn stream messages must match their owned topic and, when supplied,
the bound conversation. Encoded stream items remain opaque and unchanged.
Native `conversation-updates` batches are split into individually owned updates;
unscoped batch metadata and account-wide cursors are omitted. Conversation turn
completion and stream-message completion hints can trigger the same verified
snapshot read as async-task completion.
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

Malformed commands receive a protocol close frame rather than an abrupt reset.
Connection/closure diagnostics use fixed classifications, never frames, topic
identities or credentialed URLs. Frames are bounded (64 KiB client commands, 2 MiB provider frames), queues have
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
status. A `stream_handoff` also establishes pending work even when the provider
omits the async-status event. Only a subsequent explicit inactive status or a
verified completed conversation snapshot can clear it. The Redis seal checks pending state atomically with image evidence;
late delivery cannot change an already sealed bill.
With updates enabled, every successful delivery terminal triggers one bounded
read of its owned conversation before settlement, including streams that carry
only a resume token and omit async status. A resume token is a retry capability,
not evidence of either a handoff or final completion. Missing or unfinished
snapshot evidence leaves the turn pending for later client reads/notifications.

An owned current-branch read can bind image previews before final completion
and image downloads after settlement. Delivery evidence never adds a preview
to the bill, reopens a frozen bill or creates a second charge. A download with
missing asset ownership may use an existing scoped `conversation_id` binding
to read that original account's snapshot, then must resolve the exact asset
again. A supplied conversation ID alone grants no file access.

A native completion hint may trigger a read-only GET of the owned conversation
on its original account, at most three times per conversation per connection.
This is not another model request. Gateway-owned
delivery reads request identity encoding and remove WebSocket negotiation
headers. The JSON must prove an inactive native state (1/2/4), a successful
terminal assistant message with `end_turn=true` or a successful recognized
image-tool leaf containing a completed generated image, and a current branch
descending from the original user's message hash. Old branches, incomplete tasks, missing
schemas, errors and mismatched conversations cannot settle. Only generated
images on this new branch count; historical images do not count again. Replayed
message updates bind download ownership without adding images to a bill.

Synchronous image snapshots may omit or null `async_status`, just as native
text snapshots can. That absence alone establishes no completion. A current
branch owned by the original user message can settle only when it ends in a
successful terminal assistant or a successful recognized image-tool leaf,
every assistant/image-tool message on the new branch finished successfully,
and every deliverable image has final generated-asset evidence. Preview-only,
partially generated, unfinished, failed, arbitrary-tool and foreign branches
stay pending or fail. Any explicit active/unknown async status vetoes this
shortcut. The internal cache normalizes verified synchronous completion to
inactive; provider JSON and the client stream remain unchanged. The original
frozen base-turn and image prices settle together once, including repeated
snapshot reads. Existing pending generations are not replayed or manually
backcharged by a deployment.

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
