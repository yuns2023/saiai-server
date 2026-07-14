# Source provenance

This repository starts with a clean public history. The identifiers below are
recorded so maintainers can audit the export without publishing private
repository history or operational material. They are source identifiers, not a
claim that those Git objects are present in this public repository.

## Initial public export

- Server base snapshot:
  `d8fa095798be4513e62bb83fdf5a740b8f275a05`
- Last integrated upstream merge in that base:
  `a225a241d76b2c4f2771e28ed1fad02438505eb4`
- SAIAI V2 schema-2 candidate applied in the initial public rollout branch:
  `bba7566eed99e008e88f676679a0c6fc4a5101a9`

The integrated upstream source carried the MIT notice for
`Copyright (c) 2025 Wesley Liddick` at that point. That notice and its terms are
preserved in the repository [NOTICE](../NOTICE). The combined SAIAI Server work
is distributed under `LGPL-3.0-or-later` unless a file or third-party notice says
otherwise; the identified permissive notices remain in effect.

The clean export intentionally excludes private deployment configuration,
secrets, infrastructure inventory, traffic captures, internal research, and
the separate CLI/desktop source history. Client source is maintained in
[`yuns2023/saiai-client`](https://github.com/yuns2023/saiai-client).

## Initial schema-2 release pair

The first release-ready public server commit for the schema-2 Gateway was:

```text
b4b208f7b63256425e0c29d96acfa360526d6142
```

It was paired with `saiai-client` tag `saiai-v0.9.0`, client source commit
`abbc0e425efe4101f7180da892aaf80672bf21b6`, verified from
`saiai-v0.9.0^{commit}`, and the manifest hash recorded in
the [V2 Gateway contract](V2_GATEWAY_CONTRACT.md). These are public source and
release coordinates; deployment-specific hosts, paths, credentials, and
backup records remain outside this repository.

## Model-pricing fallback

The initial public export includes
`backend/resources/model-pricing/model_prices_and_context_window.json`, a
snapshot derived from LiteLLM's public file at:

```text
https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json
```

Initial exported-file SHA-256:

```text
dd16488fd139eaab72ea32cf87efdb48109f44dc9308c6b993a456740a113912
```

LiteLLM material outside its separately licensed enterprise directory is
published under the MIT License with `Copyright (c) 2023 Berri AI`; the
corresponding notice is preserved in [NOTICE](../NOTICE). When the fallback is
refreshed, maintainers should update the recorded snapshot hash or record the
new provenance in the change.

This document identifies known source snapshots only. It does not assert that
all dependencies, icons, fonts, generated files, or other third-party assets
share the same license. Their own source headers, lockfiles, notices, and
licenses continue to apply.
