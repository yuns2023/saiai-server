# Official Codex standalone Images

Codex 0.160.0's built-in `image_gen` extension sends separate JSON POSTs to
`/backend-api/codex/images/generations` and `/backend-api/codex/images/edits`.
It is not the ordinary Chat file-download flow or the public Images API's
OAuth-to-Responses adapter.

The local proxy maps these paths to `/v1/codex/images/*`. These two authenticated
Gateway endpoints select OpenAI OAuth accounts before acquiring their slots,
require an official Codex client and the group's Codex client policy, validate
billing eligibility and image-token pricing, and preserve the original JSON
bytes (including any zstd encoding), query, and application-header multiplicity.
Only the selected account's authorization and account ID replace the inbound
credentials; transport, proxy and cookie headers follow the native Codex
boundary policy. Supplied opaque turn state must belong to the selected account.

Responses retain the provider's status, JSON bytes, generation IDs and native
headers, including `x-codex-imagegen-request-id`. Image jobs are never
automatically replayed or failed over after an ambiguous provider error. No
synthetic `response.completed` is introduced. The acceptance attempt budget
reserves immediately before each native Images HTTP dispatch as well.

Accounting uses the requested image model and provider-returned image/text
token usage, separately from surrounding Responses turns. A missing usage
field is not a license to invent tokens: the existing image accounting path
records returned image count and only reported tokens. Operators must inspect
actual provider usage availability before claiming token billing completeness.
Raw image inputs and outputs must not enter operational request captures.

Activate a Client that forwards these endpoints only with a Gateway that
implements them. Manifest/configuration schemas remain 1, but schema equality
alone does not prove this endpoint capability. Offline proofs with synthetic
credentials establish request parity and built-in tool behavior, not live
provider access or native Desktop ordinary Chat login.
