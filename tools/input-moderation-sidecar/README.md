# SAIAI input-moderation sidecar

This optional internal service runs `Qwen/Qwen3Guard-Gen-0.6B` at immutable
revision `fada3b2f655b89601929198343c94cd2f64d93cc`. The build verifies the
1.5 GB safetensors file against SHA-256
`4f3ce47ebd968cddb67de08d8764f8ede7c410a7d1fb9e08145a4c7a2f2e5c0f`
and embeds the complete snapshot in the image. Runtime is offline and exposes
the small contract consumed by the Gateway:

```http
POST /v1/classify
Content-Type: application/json

{"text":"latest real user input"}
```

```json
{
  "safety": "Unsafe",
  "categories": ["Jailbreak"],
  "model_version": "Qwen/Qwen3Guard-Gen-0.6B@fada3b2f655b89601929198343c94cd2f64d93cc"
}
```

The sidecar receives no SAIAI user ID, email, API Key, OAuth credential, system
prompt, assistant history, or tool result. It does not log request bodies.

## Run

```bash
docker build -t saiai-input-moderation:local .
docker run --rm -p 127.0.0.1:8081:8081 --memory 4g --cpus 2 \
  saiai-input-moderation:local
```

Then set the private backend configuration:

```yaml
gateway:
  input_moderation:
    endpoint: http://input-moderation:8081/v1/classify
    worker_count: 2
    queue_size: 256
    request_timeout_seconds: 15
    max_input_chars: 32768
```

Moderation and automatic user disable remain off until enabled on an individual
group in the admin UI. The Gateway call is asynchronous: the current provider
request is not delayed or canceled. A matching unsafe incident can temporarily
cool down the site user and, after the configured number of independent strikes,
permanently disable the user and invalidate all of that user's API Key caches.

For crash-recoverable delivery, configure a fixed 64-hex-character
`totp.encryption_key` on every Gateway replica. The Gateway encrypts the complete
moderation job with AES-256-GCM before writing it to the Redis Stream. Without a
fixed key it logs the downgrade and uses only the bounded in-memory queue, since
an auto-generated key cannot decrypt pending jobs after restart.

Long text is chunked with overlap and the most severe result wins. Tune
`MAX_CHUNK_TOKENS`, `CHUNK_OVERLAP_TOKENS`, `MAX_CHUNKS`, `MAX_NEW_TOKENS`, and
`MAX_CONCURRENCY` for the available hardware. Do not expose this service on a
public interface.

The production image is CPU-only, runs as UID/GID `10001`, has no published
host port, and is addressed only by its Compose service name. A model-ready
`/healthz` endpoint drives the container health check; `/livez` only confirms
that the HTTP process is alive. The SHA-only GitHub workflow starts the image
with `--network none`, waits for model readiness, and publishes an immutable
coordinate artifact containing the source, image digest, model revision, model
hash, architecture, and image size.

Initial production rollout is primary-only. A BF16 0.6B model is not suitable
for the current 2 GB/1-CPU abapi host; keep that site's endpoint empty until a
separately validated quantized image or additional resources are available.
