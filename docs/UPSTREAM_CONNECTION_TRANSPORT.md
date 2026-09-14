# Upstream connection transport contract

The Gateway reuses outbound HTTP clients by the configured proxy/account
isolation key. Active response bodies keep their client entry in flight until
the caller closes the body; idle connections expire independently. This
lifecycle tracking must not be used as model-concurrency enforcement.

## Standard HTTP transport

The standard transport enables HTTP/2 by default even though SAIAI installs a
custom TLS root configuration:

```yaml
gateway:
  standard_upstream_http2_enabled: true
  account_aux_connection_reserve: 2
```

Without `ForceAttemptHTTP2`, Go conservatively disables HTTP/2 when a custom
TLS configuration or dialer is present. An HTTP/1.1 SSE response then occupies
one whole connection until the stream ends. Previously, account-isolated pools
also set `MaxConnsPerHost` equal to account concurrency; at concurrency one,
the long stream could block every same-account control or auxiliary request.

For standard account/account-proxy pools, the connection cap is now:

```text
account concurrency + account_aux_connection_reserve
```

Model concurrency remains enforced by the Gateway concurrency service. The
reserve only prevents the transport from becoming an accidental second queue.
HTTP/2 can multiplex streams on one connection; when ALPN falls back to
HTTP/1.1, the bounded reserve provides separate control-request headroom.

Rollback settings are:

```text
GATEWAY_STANDARD_UPSTREAM_HTTP2_ENABLED=false
GATEWAY_ACCOUNT_AUX_CONNECTION_RESERVE=0
```

The reserve is validated in the range 0 through 64.

## Explicit exclusions

- TLS fingerprint/uTLS transports keep their existing HTTP/1.1 behavior and
  do not receive the auxiliary connection reserve.
- OpenAI Responses WebSocket uses its own dialer, pool, waiters, ping, and
  acquire-timeout contracts.
- `ResponseHeaderTimeout` ends when response headers arrive. SSE body-idle and
  terminal handling remain service-layer responsibilities.

## Observability

Every outbound request attaches a content-free `net/http/httptrace` observer.
The debug marker `http_upstream_transport` records:

- transport kind, account concurrency, and whether a proxy was used;
- negotiated response protocol;
- `GetConn` to `GotConn` wait time;
- connection reuse/idle state; and
- whether the request failed before a response.

The info marker `http_upstream_transport_metrics` emits aggregate counts every
1024 outcomes. Metrics include request/GotConn counts, total connection wait,
new/reused connections, HTTP/1/HTTP/2/other responses, and request errors. No
URL, header, credential, or request/response body is recorded by these markers.

## Regression requirements

Local TLS fixtures must prove all of the following before release:

- an H2 control request reaches the upstream while a same-account SSE stream
  remains open with `MaxConnsPerHost=1`;
- an H1 control request reaches the upstream through the auxiliary reserve;
- direct, HTTP CONNECT proxy, and SOCKS5H paths negotiate H2 when supported;
- the rollback switch disables H2;
- account/proxy isolation and idle/in-flight eviction behavior remain intact;
  and
- TLS fingerprint pool sizing is unchanged.

Production attribution still requires timing each hop. A delay before Gateway
ingress is not an outbound-pool delay; a delay after `GotConn` and request write
belongs to the provider/proxy response phase.
