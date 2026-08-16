# OpenAI Image API gateway

The gateway exposes the direct OpenAI-compatible endpoints:

- `POST /v1/images/generations` (`application/json`)
- `POST /v1/images/edits` (`multipart/form-data`)

For API-key accounts, the request body and safe client headers are forwarded
without schema translation. Consequently, custom OpenAI-compatible image model
IDs and future request fields do not require a gateway release on that native
path. `gpt-image-2` is included in the curated model catalog for discovery, but
it is not a protocol allowlist.
In standard billing mode, a model must have complete text-input, image-input,
and image-output token pricing before the request is sent upstream.

## Routing and safety boundary

- OpenAI API-key accounts, including accounts with an approved custom
  `base_url`, receive byte-preserving native Images API passthrough.
- ChatGPT/Codex OAuth accounts are also eligible. The Gateway converts the
  public Images request to a Codex Responses request with an
  `image_generation` tool, drains the internal SSE response, and converts the
  result back to the Images response shape.
- OAuth image execution is not identical to the native Images API. The
  upstream may report a different effective model, size, or quality (for
  example `gpt-image-2-codex` with automatic settings). The Gateway reports
  observed upstream metadata instead of claiming the requested settings were
  honored.
- Direct Image API streaming is rejected. Non-streaming JSON responses are
  bounded by `gateway.openai_images_response_read_max_bytes` (64 MiB by
  default); request bodies remain bounded by `gateway.max_body_size`.
- Image edit bodies are never placed into operational body-capture context.
  Logs contain routing metadata, counts, and timing only.
- Billing uses the upstream response's text-input, image-input, and
  image-output token categories with the corresponding pricing fields. It does
  not use the legacy guessed per-image fallback.

The public `/v1/responses` path remains independent. This bridge translates
Images API ingress to the existing OAuth Responses upstream; it does not
rewrite arbitrary client-authored Responses requests.

## Client examples

The caller always uses its SAIAI Key. It does not need to know whether the
selected upstream account is OAuth or API-key based.

```bash
curl "$SAIAI_BASE_URL/v1/images/generations" \
  -H "Authorization: Bearer $SAIAI_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-image-2","prompt":"a watercolor lighthouse at dusk","size":"1024x1024","response_format":"b64_json"}'
```

Image edits use multipart form data:

```bash
curl "$SAIAI_BASE_URL/v1/images/edits" \
  -H "Authorization: Bearer $SAIAI_KEY" \
  -F "model=gpt-image-2" \
  -F "prompt=replace the sky with a sunset" \
  -F "image=@source.png" \
  -F "response_format=b64_json"
```
