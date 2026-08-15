# OpenAI Image API gateway

The gateway exposes the direct OpenAI-compatible endpoints:

- `POST /v1/images/generations` (`application/json`)
- `POST /v1/images/edits` (`multipart/form-data`)

The request body and safe client headers are forwarded without schema
translation. Consequently, custom OpenAI-compatible image model IDs and future
request fields do not require a gateway release. `gpt-image-2` is included in
the curated model catalog for discovery, but it is not a protocol allowlist.
In standard billing mode, a model must have complete text-input, image-input,
and image-output token pricing before the request is sent upstream.

## First-phase safety boundary

- Only OpenAI API-key accounts, including accounts with an approved custom
  `base_url`, are eligible. ChatGPT/Codex OAuth accounts are skipped because
  their internal Responses protocol is not the public Image API.
- Direct Image API streaming is rejected. Non-streaming JSON responses are
  bounded by `gateway.openai_images_response_read_max_bytes` (64 MiB by
  default); request bodies remain bounded by `gateway.max_body_size`.
- Image edit bodies are never placed into operational body-capture context.
  Logs contain routing metadata, counts, and timing only.
- Billing uses the upstream response's text-input, image-input, and
  image-output token categories with the corresponding pricing fields. It does
  not use the legacy guessed per-image fallback.

This phase does not add `/v1/responses` image-tool translation. Clients that use
the Responses image generation tool continue through the existing Responses
endpoint and its own protocol.
