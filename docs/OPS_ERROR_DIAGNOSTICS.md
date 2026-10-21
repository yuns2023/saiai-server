# Ops error diagnostics

The admin Ops error detail dialog presents captured failure context in a table,
alongside the original response and upstream attempts. Context includes phase,
source, owner, separate gateway/client request IDs, transport and upstream status,
client User-Agent, model, captured request size, timings and available WebSocket
turn/close/hash evidence. Absent information is not inferred from error messages.

Codex Responses admission records the checks it evaluates for the selected
group policy. The field table shows official User-Agent, optional originator
recognition, terminal client shape
and, for `local_proxy_only`, presence of `version` and `chatgpt-account-id`.
Successful admission checks remain available when an upstream subsequently
fails. Model discovery does not run these identity checks. This instrumentation
does not change admission rules, HTTP responses or WebSocket close frames.

Snapshots use schema version 1 in nullable `ops_error_logs.diagnostics` JSONB.
The service normalizes an explicit allowlist before persistence and the reader
normalizes it again. Identity values, credentials and bodies are excluded;
client versions must match the supported numeric prerelease syntax. This is
independent of opt-in full request capture. It does not enable body capture.

Migration **109_ops_error_diagnostics.sql** must run before starting the new
Gateway. Retain the nullable column on application rollback; no restore or
backfill is needed. Historical records show available context and explicitly
state that field checks were not captured. Provider errors do not acquire
invented missing-field diagnoses; their response remains the source of detail.

Internal gateway request IDs come from request context even on compatibility
paths that omit the response `X-Request-Id`. Upstream correlation stays separate.

Error snapshots also record `request_host` (the Host received by the Gateway)
and `forwarded_host` (the single proxy-reported `X-Forwarded-Host`). They are
collected without reading the body, so failed uploads retain this context.
The detail summary shows the ingress domain; the context table keeps both
values separate. Older snapshots without the domain show "Not captured".
No new migration beyond 109 is needed for these optional schema-1 fields.

Domains are observational evidence, never authorization or redirect inputs.
Normalization permits only bounded DNS names and IP addresses, removes ports,
and rejects URLs, credentials, paths, queries and ambiguous header lists.
Proxy deployments must overwrite untrusted forwarded hosts at the outer edge
and preserve the original value through subsequent trusted hops. A rewritten
Host alone cannot identify the public ingress, and existing records cannot be
backfilled from the request path. This feature does not change proxy settings
or enable full-header/body capture.
