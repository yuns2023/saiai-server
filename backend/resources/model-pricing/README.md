# Model Pricing Data

This directory contains the image-bundled pricing snapshot used as the runtime fallback file.

## Source Invariant
Only one pricing source should exist at a time.

- Runtime remote source: `pricing.remote_url` in `backend/internal/config/config.go`
- Runtime fallback file: `backend/resources/model-pricing/model_prices_and_context_window.json`

These two files must always be from the same source snapshot family. Do not point
`pricing.remote_url` at one source while leaving this fallback file generated from
another source.

## Purpose
This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process
The pricing service will:
1. First attempt to download `pricing.remote_url`
2. If download fails, use this local file as fallback
3. Log a warning when using the fallback file
4. During runtime, refresh from the remote source on the scheduler interval:
   - with `pricing.hash_url`, compare the remote hash first
   - without `pricing.hash_url`, download the remote JSON and compare content
     hash, only rewriting the local cache when it changed

This only stays correct if the fallback file is refreshed from the same
`pricing.remote_url`.

## Sync Commands
Use the repository helper instead of ad-hoc curl commands:

```bash
bash tools/pricing_fallback_sync.sh check
bash tools/pricing_fallback_sync.sh check --semantic [--billed-keys FILE]
bash tools/pricing_fallback_sync.sh sync
```

- `check` compares the current remote source against the bundled fallback
  snapshot byte-for-byte (use it to confirm a fresh `sync`)
- `check --semantic` reports added/removed models and changed billing fields
  (`tools/pricing_fallback_diff.py`), validating only the fields the Gateway
  parser decodes. It fails when the download fails or a feed is unusable
  (not JSON, too few priced models, or a large shrink) or, with
  `--billed-keys` (one resolved price key per line), when a billed key was
  removed, is missing from the fallback, changed a billing field, or has an
  ambiguous billing field (repeated or spelled several ways). A missing
  price equals an explicit 0, as in the parser. Drift alone, odd values in
  other metadata, and a fallback whose last sync is more than 7 days old
  only warn.
- `sync` refreshes the bundled fallback file from `pricing.remote_url`

The read-only `Pricing Fallback Check` GitHub Actions workflow (job `check`)
runs the semantic check on `main`, the daily schedule and manual runs, so
upstream drift alone does not turn `main` red (a failed download, failed
helper tests or an unusable feed still do). Pull requests that change the
snapshot or the default `pricing.remote_url` must be an exact copy of the
remote; pull requests that change only the pricing tooling run the semantic
check. Maintainers run the sync command locally and submit the updated
snapshot through a reviewed pull request.

## Reference Corrections
Do not hand-edit the synced JSON: `sync` overwrites it and runtime refreshes
replace it with `pricing.remote_url` data. Corrections for clear feed errors
live in `backend/internal/service/pricing_reference_corrections.go` and run
when any snapshot (remote or bundled) is parsed. Administrator aliases and
price overrides still apply on top.

- OpenAI models without a Priority/Fast tier: OpenAI's pricing page
  (checked 2026-10-22; Priority was renamed Fast on 2026-07-30) lists these
  in a Standard table but not in the matching Fast table:
  `gpt-5.5-pro`, `gpt-5.4-nano`, `gpt-5.4-pro`, `gpt-5.2-pro`, `gpt-5-nano`,
  `gpt-5-pro`, `o1`, `o1-pro`, `o3-pro`, `o3-mini`, `gpt-4-turbo-2024-04-09`,
  `gpt-4-0613`, `gpt-3.5-turbo`, `gpt-3.5-turbo-0125` (main text-token table),
  and `chat-latest`, `gpt-5-search-api` (specialized models table). Exact
  names and dated snapshots (`-YYYY-MM-DD`/`-YYYYMMDD`) match;
  provider-prefixed entries (`azure/...`) are not affected. Their feed
  Priority fields are dropped (the feed copied `gpt-5`'s 2.5 USD/M Priority
  input onto `gpt-5-nano`), and the entry is marked so that a Priority request
  bills at Standard instead of the generic x2 multiplier. Flex is unchanged.
  Administrator Priority overrides still apply; see
  `docs/MODEL_PRICING_MANAGEMENT.md`. The cyber models table has no Fast view
  at all, so its models are not listed here.

`TestBundledPricing_PriorityMatchesOfficialFastPrices` pins the official
OpenAI Priority (Fast) prices of the listed models, both in the raw bundled
entries and in billing. A sync that changes them fails that test and needs
review against the official pricing page. Runtime remote refreshes are not
checked by this test; they still pass through the corrections above.

## Official Pricing Note
For OpenAI, this repository currently does not consume an official machine-readable
pricing JSON feed. If you later introduce an official or self-maintained canonical
mirror, update `pricing.remote_url` first, then run `tools/pricing_fallback_sync.sh sync`
so the bundled fallback file remains same-source.

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities

Last updated: 2026-03-22
