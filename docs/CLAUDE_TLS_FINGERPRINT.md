# Claude model-request TLS fingerprint

The built-in profiles reproduce the observed initial ClientHello of **Linux
x86_64 native Claude Code model requests**, including the distinct 2.1.301
and 2.1.302 shapes. They are not universal Node.js, IDE or operating-system
fingerprints.

Anthropic OAuth and setup-token accounts enable fingerprinting when
`extra.enable_tls_fingerprint` is absent. New accounts created through the
WebUI also default to enabled. An explicit `false` remains an opt-out, including
on existing accounts; no data migration changes existing choices. Other
account types do not acquire this account-level feature. The request path requires both the global `gateway.tls_fingerprint.enabled`
switch (default true) and the account switch. Profiles are selected automatically
from the current outbound Claude Code User-Agent, independently of account ID.
Configuration changes require a process restart.
Explicitly disabling the global switch selects the standard Go transport.

## Minimum supported version and measured transition

The default minimum Claude Code version for a fresh installation is **2.1.257**.
This uses the existing administrator-controlled `min_claude_code_version` gate;
it does not introduce a second hard-coded admission check. Existing installations
retain their stored settings. The release does not require overwriting an
existing administrator minimum or maximum. If an operator separately chooses
to admit clients from the measured 2.1.257 boundary, review both limits together;
that policy change is independent of deploying the TLS implementation. This
source change alone does not update a running deployment's settings.

Official npm publication times (UTC):

| Version | Published | Model-request TLS |
| --- | --- | --- |
| 2.1.252 | 2026-08-31 17:07:28.168 | Old: X25519 only, padding |
| 2.1.257 | 2026-09-01 17:15:33.223 | New: hybrid + X25519, no padding |

The official npm version list has no published 2.1.253–2.1.256 entries.
Binary search tested 2.1.280, 2.1.236, 2.1.259, 2.1.245, 2.1.250,
2.1.252, and 2.1.257. Both adjacent published boundary versions then passed
three direct and three HTTP CONNECT model-request captures. All recorded
structural fields of 2.1.252 match 2.1.200; all those of 2.1.257 match 2.1.300,
including cipher/extension ordering, key-share lengths, ALPN, signature
algorithms and record length. This is a bisected transition, not an exhaustive
scan proving there were no earlier temporary changes or later reversions.
The claim remains limited to the tested Linux x86_64 native model-request path.

Sanitized boundary captures and verified native artifact SHA-256 values are in
`backend/internal/pkg/tlsfingerprint/testdata/claude-2.1.252-linux-model-hello.json`
and `claude-2.1.257-linux-model-hello.json`. Official sources:
[version publication metadata](https://registry.npmjs.org/@anthropic-ai/claude-code),
[2.1.257 native manifest](https://storage.googleapis.com/claude-code-dist-86c565f3-f756-42ad-8dfa-d59b1c096819/claude-code-releases/2.1.257/manifest.json).
All 19 model requests in the boundary search were served by an isolated local
mock with external networking disabled, synthetic authentication, and certificate
verification enabled. No real provider/model request was made.

## Wire baseline

The structural fixture is
`backend/internal/pkg/tlsfingerprint/testdata/claude-2.1.300-linux-model-hello.json`.
It contains no credentials, random bytes, session tickets, or key material.

Baseline provenance:

- Official native artifact SHA-256:
  `674deba0bba9aee479f4503c438b49303b6fdd304b9a471b14977aaec5215453`.
- CLI print mode, synthetic `CLAUDE_CODE_OAUTH_TOKEN`, fresh configuration,
  Sonnet 4.6, tools disabled, no saved OAuth login state.
- Official `api.anthropic.com` URL retained, no `ANTHROPIC_BASE_URL` override;
  isolated hosts routing to a trusted local HTTPS mock, with external network
  access disabled.
- Three direct and three HTTP CONNECT runs; each selected ClientHello was
  correlated with the connection carrying `POST /v1/messages?beta=true`.
  Control-plane requests from the same binary had different TLS shapes.

The observed model ClientHello offers 17 cipher suites, TLS 1.3/1.2, ALPN
`http/1.1`, and supported groups `4588,29,23,24` (X25519MLKEM768, X25519,
P-256, P-384). Key shares are X25519MLKEM768 (1216 bytes) followed by X25519
(32 bytes). It has neither ECH GREASE nor padding. Its JA3 is
`1523504b38f0fae0d881d4b6554aac1b`.

The previous default offered only X25519/P-256/P-384, one X25519 key share,
and additional ECH GREASE and padding extensions. Updating only the account
switch could not correct those differences. The updated profile changes the
handshake; HTTP request bodies and headers are not rewritten by this change.

Administrator-defined TLS templates and account-ID-based template assignment
are removed. Remove the legacy `gateway.tls_fingerprint.profiles` key (including
an empty map) and its environment equivalent before upgrading. Startup rejects
that setting with a migration message rather than silently ignoring it. Keep
`gateway.tls_fingerprint.enabled` and account opt-outs as needed; no database
migration or change to stored account choices is required.

## Validation and future changes

`TestDefaultClientHelloMatchesClaude2300` captures the actual emitted TLS
record and compares ordered fields and key-share lengths against the native
fixture using an independent parser. It exercises both a nil/default profile
and the built-in default. It does not merely compare preset structs.
`TestDefaultClientHelloCompletesVerifiedHandshake` verifies local TLS and
HTTP completion with certificate verification enabled, covering hybrid and
classical TLS 1.3 key exchange and TLS 1.2. Custom-group tests ensure no unadvertised key share
is inserted.

Run these tests without external services:

```sh
cd backend
GOMAXPROCS=2 go test -p 1 -tags=unit -short ./internal/pkg/tlsfingerprint
```

When changing the baseline, capture the exact official client version again,
correlate the model request with its TLS connection, and retain sanitized
ordered fields plus artifact identity. Include direct and proxy paths. Do not
replace the fixture with the Gateway's own output to make a failing test pass.
Client version monitoring must validate TLS separately from HTTP body/CCH
parity. This Server package does not discover official-client releases; the
separate compatibility monitor performs discovery and pinned-probe comparisons.

Matching this fixture or JA3 does not prove complete TLS behavior, resumption,
HTTP/2 behavior, provider acceptance, or any account-risk outcome. A later
TLS-terminating proxy creates a new provider-facing handshake. Production
runtime profiles and egress chains need separate observation.

## HTTPS proxies and client headers

An `https://` proxy establishes a verified TLS connection to the proxy before
CONNECT, then performs the fingerprinted target TLS handshake inside the tunnel.
Both layers validate certificates and hostnames. HTTP CONNECT and SOCKS5 remain
separate routes. CONNECT honors context cancellation/deadlines; the connection's
setup deadline is cleared after the target handshake.

Shared and single_device device fingerprints no longer overwrite the current
request's User-Agent, feature beta, SDK/runtime version or dynamic SDK headers.
Explicit administrator x-stainless overrides still apply afterward. Identity
mapping and cached OS/architecture behavior remain; this is not byte-transparent
forwarding. All OAuth modes preserve cc_version, absent CCH and placeholder CCH;
non-placeholder CCH uses the matched input profile after body rewriting.

`backend/cmd/claude-tls-probe` is an offline compatibility executable using the
same default dialer as the Gateway. It only accepts numeric loopback HTTP proxy
URLs and a trusted local certificate, and sends a synthetic local model request.
It neither loads account credentials nor tests a live deployed Gateway. Pin its
binary hash and Server commit in scheduled-monitor evidence, and refresh it when
the relevant Gateway transport changes. Scheduled native/probe comparisons must
retain an independent official baseline rather than updating expected values
from the probe's own output.

## Claude Code 2.1.301 and 2.1.302

The built-in profile selects the model ClientHello from the outbound Claude Code
User-Agent. Versions through 2.1.301 retain nine signature schemes; 2.1.302 and
later add 0x0807 and 0x0603 before 0x0201. Unknown or malformed User-Agents retain
the legacy default. The newest known profile is a fallback for later versions,
not proof of future compatibility.

Independent Linux x86_64 model-connection captures for both versions are retained
under tlsfingerprint/testdata with native artifact SHA-256 values. Two captures
per version agreed. The 2.1.302 record increases from 1504 to 1508 bytes while
JA3 stays unchanged. Wire tests compare ordered structure, including signature
schemes and key-share lengths.

Selection copies the built-in profile. Global/account TLS switches still apply. Pool keys include the variant:
interleaved old/new requests cannot reuse each other's TLS connections. There
are two variants, rather than a new pool per client version.

The offline probe accepts --user-agent for the same selection. Monitor adoption
must pass the candidate User-Agent and select an independently captured version
baseline. Updating only the probe does not update the deployed monitor's fixed
comparison. Monitor activation and Gateway deployment are separate operations.
Windows/macOS/IDE parity and live provider acceptance remain untested.
