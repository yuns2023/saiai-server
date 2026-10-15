# Minimal request transformations

Native provider requests retain client semantics. Authentication replacement,
account ownership checks, transport framing and explicitly configured routing
policies remain Gateway responsibilities. Provider validation errors must not
silently become successful requests with reduced input.

## OpenAI Responses

OpenAI API-key HTTP requests, like official Codex OAuth/native-relay HTTP requests,
retain the original body. This includes `max_output_tokens`, `max_completion_tokens`,
`reasoning.effort`, `prompt_cache_retention`, `safety_identifier`,
`previous_response_id`, unknown fields, JSON number precision and formatting.
The upstream, not a client User-Agent heuristic, decides parameter validity.
The Forward HTTP/WS recovery paths for these native accounts do not delete continuation anchors or
encrypted reasoning after a provider rejection. Ordinary transport retries keep
their existing policy. This does not claim byte-identical JSON serialization
through the legacy WebSocket adapter.

This body contract does not grant API-key callers OAuth account privileges or
relax official-client admission, pricing, quotas or ownership validation. Other
legacy protocol adapters and response transformations are outside this change.

## Claude OAuth/setup-token

The native Messages path preserves the client's `cache_control` layout, including
layouts the upstream may reject. It does not remove thinking-block cache fields
or trim cache points to a locally hard-coded count. Necessary identity rewriting
and CCH handling remain intact. Legacy non-passthrough API-key compatibility
retains its existing cache rectifier.

## Anthropic API-key passthrough

Messages and count_tokens retain the incoming raw query (including its absence,
ordering, duplicates and escaping), without injecting `beta=true`. The selected
upstream base URL and supported endpoint still determine the destination.
Unknown application headers and repeated values pass through. Authentication,
cookies, proxy/internal headers, hop-by-hop headers and all headers named by
`Connection` do not. The selected account's API key is applied after filtering.
Existing missing content-type/version defaults remain.

Explicit account model mapping and group environment policies remain separate
opt-ins; "passthrough" does not override those administrator choices. Response
header policy is unchanged. Query-based SAIAI authentication remains rejected
by ingress middleware.

## Validation boundary

Local regression tests exercise mocked provider egress, HTTP/WS error recovery,
raw body preservation, OAuth cache layout, query fidelity and credential/header
isolation. They do not establish live provider acceptance, official Desktop/IDE
compatibility, or production activation. No live model request is required.
