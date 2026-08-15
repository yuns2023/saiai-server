# OpenAI/Codex model passthrough policy

SAIAI treats upstream model discovery, request admission, billing metadata, and
protocol support as separate concerns.

## Discovery and execution

- Codex clients requesting `/v1/models?client_version=...` or
  `/backend-api/codex/models` receive the selected OpenAI account's live Codex
  manifest.
- OAuth manifests are envelope-validated and otherwise passed through.
- Custom API-key providers may expose either a Codex manifest or a standard
  OpenAI `/v1/models` list. SAIAI performs only the minimum shape conversion
  required by Codex and keeps a bounded short/stale cache.
- The static `/v1/models` catalog remains a compatibility/display fallback for
  non-Codex clients. It is not an execution allowlist.
- `/v1/responses` preserves the requested model name. The selected upstream,
  not SAIAI's static catalog, decides whether that model is executable.

## Missing pricing

A successful OpenAI response whose final billing model has no pricing metadata
is retained as a usage row with its real model and token counts and zero cost.
SAIAI emits a structured warning so operators can add pricing or reconcile the
usage later. Calculation and storage errors still fail normally; only the
typed missing-pricing condition receives this treatment.

To bound exposure, successful zero-cost usage increments a per-model rolling
window counter. Redis shares the counter across gateway replicas, while every
replica also keeps a local fallback counter. Once the threshold is reached,
subsequent requests for that still-unpriced model receive HTTP 429 until the
window expires or pricing becomes available.

```yaml
gateway:
  openai_unpriced_model_max_successes: 100
  openai_unpriced_model_window_seconds: 3600
```

Set `openai_unpriced_model_max_successes` to `0` to disable the circuit breaker
while retaining passthrough and zero-cost usage recording.

## Deliberate boundary

Text/Responses model support does not imply image protocol support.
`gpt-image-2` requires its image generation/editing endpoints, payloads,
responses, and per-image billing to be implemented and validated as a separate
change before SAIAI advertises it as a supported executable model.
