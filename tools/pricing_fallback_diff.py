#!/usr/bin/env python3
"""Semantic comparison of the bundled pricing fallback against the remote feed.

The bundled LiteLLM snapshot is only the Gateway's cold-start fallback: running
Gateways download pricing.remote_url and refresh it every few minutes. A byte
difference therefore says nothing about billing by itself. This helper reports
what changed in the fields the Gateway parser reads and fails only when:

* either document is not a valid pricing feed (exit 2): not a JSON object,
  duplicate keys, non-finite numbers, an entry the Go parser would reject,
  fewer than --min-priced-models usable entries, or a remote that lost more
  than --max-shrink-ratio of the fallback's usable entries; or
* --billed-keys is given and a billed key is affected (exit 1): removed from
  the remote, missing from the fallback, or with different billing fields.

Everything else (added models, metadata-only drift, unread cost fields) is a
warning. With --fallback-age-days, drift on a fallback older than
--max-age-days adds a staleness warning; it never fails.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
import time
from pathlib import Path
from typing import Any

# Fields decoded by LiteLLMRawEntry in backend/internal/service/pricing_service.go.
FLOAT_FIELDS = (
    "input_cost_per_token",
    "input_cost_per_token_above_272k_tokens",
    "input_cost_per_token_priority",
    "output_cost_per_token",
    "output_cost_per_token_above_272k_tokens",
    "output_cost_per_token_priority",
    "cache_creation_input_token_cost",
    "cache_creation_input_token_cost_above_272k_tokens",
    "cache_creation_input_token_cost_priority",
    "cache_creation_input_token_cost_above_1hr",
    "cache_read_input_token_cost",
    "cache_read_input_token_cost_above_272k_tokens",
    "cache_read_input_token_cost_priority",
    "long_context_input_cost_multiplier",
    "long_context_output_cost_multiplier",
    "long_context_cache_creation_cost_multiplier",
    "long_context_cache_read_cost_multiplier",
    "output_cost_per_image",
    "input_cost_per_image_token",
    "output_cost_per_image_token",
)
INT_FIELDS = ("long_context_input_token_threshold",)
BOOL_FIELDS = ("supports_service_tier", "supports_prompt_caching")
STRING_FIELDS = ("litellm_provider", "mode")
BILLING_FIELDS = FLOAT_FIELDS + INT_FIELDS + BOOL_FIELDS + STRING_FIELDS
DOC_KEYS = ("sample_spec",)

INT64_MIN, INT64_MAX = -(2**63), 2**63 - 1

EXIT_PASS = 0
EXIT_BLOCK = 1
EXIT_INVALID = 2


class FeedError(ValueError):
    """The document is not a pricing feed the Gateway can use."""


def _reject_constant(value: str) -> Any:
    raise FeedError(f"non-finite number {value}")


def _finite_float(text: str) -> float:
    number = float(text)
    if not math.isfinite(number):
        raise FeedError(f"number {text} overflows float64")
    return number


def go_fields(name: str, entry: dict[str, Any]) -> dict[str, Any]:
    """Map JSON keys to LiteLLMRawEntry fields the way encoding/json does.

    Go matches struct tags case-insensitively and a later key overwrites an
    earlier one, so {"input_cost_per_token": 1, "INPUT_COST_PER_TOKEN": 9}
    decodes to 9. Non-ASCII keys that fold onto a known field are rejected
    instead of emulating Go's special Unicode folding.
    """
    fields: dict[str, Any] = {}
    for key, value in entry.items():
        if key in BILLING_FIELDS:
            fields[key] = value
            continue
        folded = key.lower() if key.isascii() else key.casefold()
        if folded in BILLING_FIELDS:
            if not key.isascii():
                raise FeedError(f"{name}: non-ASCII key {key!r} folds onto {folded}")
            fields[folded] = value
    return fields


def _no_duplicates(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise FeedError(f"duplicate key {key!r}")
        result[key] = value
    return result


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def billing_view(name: str, entry: Any) -> dict[str, Any] | None:
    """Return the Go parser's view of one entry, or None when it is skipped.

    Go skips entries that do not decode into LiteLLMRawEntry (wrong field type
    or non-object) and entries without an input or output token price; absent
    and null decode identically.
    """
    if not isinstance(entry, dict):
        return None
    entry = go_fields(name, entry)
    view: dict[str, Any] = {}
    for field in FLOAT_FIELDS:
        value = entry.get(field)
        if value is None:
            view[field] = None
        elif _is_number(value):
            try:
                number = float(value)
            except OverflowError:
                number = math.inf
            if not math.isfinite(number):
                raise FeedError(f"{name}.{field} is not a finite float64")
            view[field] = number
        else:
            return None
    for field in INT_FIELDS:
        value = entry.get(field)
        if value is None:
            view[field] = None
        elif isinstance(value, int) and not isinstance(value, bool) and INT64_MIN <= value <= INT64_MAX:
            view[field] = value
        else:
            return None
    for field in BOOL_FIELDS:
        value = entry.get(field)
        if value is None:
            view[field] = False
        elif isinstance(value, bool):
            view[field] = value
        else:
            return None
    for field in STRING_FIELDS:
        value = entry.get(field)
        if value is None:
            view[field] = ""
        elif isinstance(value, str):
            view[field] = value
        else:
            return None
    if view["input_cost_per_token"] is None and view["output_cost_per_token"] is None:
        return None
    return view


def load_feed(text: str, label: str) -> dict[str, Any]:
    try:
        document = json.loads(
            text, object_pairs_hook=_no_duplicates, parse_constant=_reject_constant,
            parse_float=_finite_float,
        )
    except FeedError as error:
        raise FeedError(f"{label}: {error}") from None
    except ValueError as error:
        raise FeedError(f"{label}: not valid JSON ({error})") from None
    if not isinstance(document, dict):
        raise FeedError(f"{label}: top level is not a JSON object")
    return document


def priced_views(document: dict[str, Any], label: str) -> dict[str, dict[str, Any]]:
    views: dict[str, dict[str, Any]] = {}
    for name, entry in document.items():
        if name in DOC_KEYS:
            continue
        try:
            view = billing_view(name, entry)
        except FeedError as error:
            raise FeedError(f"{label}: {error}") from None
        if view is not None:
            views[name] = view
    return views


def unread_cost_fields(entry: Any) -> dict[str, Any]:
    if not isinstance(entry, dict):
        return {}
    return {
        key: value
        for key, value in entry.items()
        if "cost" in key and key not in BILLING_FIELDS
    }


def compare(fallback: dict[str, Any], remote: dict[str, Any]) -> dict[str, Any]:
    fallback_views = priced_views(fallback, "fallback")
    remote_views = priced_views(remote, "remote")
    added = sorted(set(remote_views) - set(fallback_views))
    removed = sorted(set(fallback_views) - set(remote_views))
    billing_changed: dict[str, list[str]] = {}
    unread_cost_changed: list[str] = []
    metadata_only: list[str] = []
    for name in sorted(set(fallback_views) & set(remote_views)):
        old_view, new_view = fallback_views[name], remote_views[name]
        fields = [field for field in BILLING_FIELDS if old_view[field] != new_view[field]]
        if fields:
            billing_changed[name] = fields
        elif unread_cost_fields(fallback[name]) != unread_cost_fields(remote[name]):
            unread_cost_changed.append(name)
        elif fallback[name] != remote[name]:
            metadata_only.append(name)
    unpriced_changed = sorted(
        name
        for name in set(fallback) | set(remote)
        if name not in fallback_views
        and name not in remote_views
        and fallback.get(name) != remote.get(name)
    )
    return {
        "fallback_priced_models": len(fallback_views),
        "remote_priced_models": len(remote_views),
        "added": added,
        "removed": removed,
        "billing_changed": billing_changed,
        "unread_cost_changed": unread_cost_changed,
        "metadata_only_changed": len(metadata_only),
        "unpriced_or_doc_changed": len(unpriced_changed),
        "drift": bool(
            added or removed or billing_changed or unread_cost_changed
            or metadata_only or unpriced_changed
        ),
    }


def billed_impact(report: dict[str, Any], fallback: dict[str, Any], remote: dict[str, Any],
                  billed: set[str]) -> dict[str, list[str]]:
    fallback_views = priced_views(fallback, "fallback")
    remote_views = priced_views(remote, "remote")
    removed = sorted(billed & set(report["removed"]))
    missing = sorted(key for key in billed if key in remote_views and key not in fallback_views)
    changed = sorted(billed & set(report["billing_changed"]))
    unknown = sorted(key for key in billed if key not in remote_views and key not in fallback_views)
    return {
        "billed_removed": removed,
        "billed_missing_from_fallback": missing,
        "billed_cost_changed": changed,
        "billed_in_neither": unknown,
    }


def read_billed_keys(path: Path) -> set[str]:
    keys = {line.strip() for line in path.read_text(encoding="utf-8").splitlines()}
    keys.discard("")
    if not keys:
        raise FeedError(f"billed key file {path} is empty")
    return keys


def safe(name: str) -> str:
    """Render an upstream key without letting it forge CI annotations."""
    return json.dumps(name, ensure_ascii=True)


def render(report: dict[str, Any], limit: int = 40) -> list[str]:
    def listing(keys: list[str]) -> str:
        shown = ", ".join(safe(key) for key in keys[:limit])
        return shown + (f", ... (+{len(keys) - limit})" if len(keys) > limit else "")

    lines = [
        f"priced models: fallback={report['fallback_priced_models']} remote={report['remote_priced_models']}",
        f"added={len(report['added'])} removed={len(report['removed'])} "
        f"billing_changed={len(report['billing_changed'])} "
        f"unread_cost_changed={len(report['unread_cost_changed'])} "
        f"metadata_only_changed={report['metadata_only_changed']} "
        f"unpriced_or_doc_changed={report['unpriced_or_doc_changed']}",
    ]
    if report["added"]:
        lines.append("added: " + listing(report["added"]))
    if report["removed"]:
        lines.append("removed: " + listing(report["removed"]))
    changed = report["billing_changed"]
    for name in list(changed)[:limit]:
        lines.append(f"billing changed: {safe(name)} fields={','.join(changed[name])}")
    if len(changed) > limit:
        lines.append(f"billing changed: ... (+{len(changed) - limit})")
    if report["unread_cost_changed"]:
        lines.append("unread cost fields changed: " + listing(report["unread_cost_changed"]))
    return lines


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--fallback", type=Path, required=True)
    parser.add_argument("--remote", type=Path, required=True)
    parser.add_argument("--billed-keys", type=Path, help="one resolved price key per line")
    parser.add_argument("--min-priced-models", type=int, default=1000)
    parser.add_argument("--max-shrink-ratio", type=float, default=0.2)
    parser.add_argument("--fallback-synced-at", type=int,
                        help="Unix time of the fallback's last sync commit")
    parser.add_argument("--max-age-days", type=float, default=7.0)
    parser.add_argument("--json", type=Path, help="write the machine-readable report here")
    parser.add_argument("--github", action="store_true", help="emit GitHub Actions annotations")
    args = parser.parse_args(argv)
    if args.min_priced_models < 1:
        parser.error("--min-priced-models must be positive")
    if not 0 <= args.max_shrink_ratio < 1:
        parser.error("--max-shrink-ratio must be in [0, 1)")
    if not args.max_age_days > 0:
        parser.error("--max-age-days must be positive")

    try:
        fallback = load_feed(args.fallback.read_text(encoding="utf-8"), "fallback")
        remote = load_feed(args.remote.read_text(encoding="utf-8"), "remote")
        report = compare(fallback, remote)
        billed = read_billed_keys(args.billed_keys) if args.billed_keys else None
    except (FeedError, OSError, UnicodeDecodeError) as error:
        print(f"INVALID: {error}", file=sys.stderr)
        return EXIT_INVALID

    problems: list[str] = []
    for label in ("fallback", "remote"):
        count = report[f"{label}_priced_models"]
        if count < args.min_priced_models:
            problems.append(f"{label} has {count} priced models (< {args.min_priced_models})")
    floor = report["fallback_priced_models"] * (1 - args.max_shrink_ratio)
    if report["remote_priced_models"] < floor:
        problems.append(
            f"remote lost more than {args.max_shrink_ratio:.0%} of the fallback's priced models"
        )

    warnings: list[str] = []
    blocking: list[str] = []
    if billed is not None:
        impact = billed_impact(report, fallback, remote, billed)
        report["billed_keys"] = len(billed)
        report.update(impact)
        for field, text in (
            ("billed_removed", "billed key removed from remote"),
            ("billed_missing_from_fallback", "billed key missing from fallback"),
            ("billed_cost_changed", "billed key billing fields changed"),
        ):
            if impact[field]:
                blocking.append(f"{text}: " + ", ".join(safe(key) for key in impact[field]))
        if impact["billed_in_neither"]:
            warnings.append(
                "billed key in neither feed (alias/override or retired; review): "
                + ", ".join(safe(key) for key in impact["billed_in_neither"])
            )
    if report["drift"]:
        warnings.append("fallback differs from remote (not a failure by itself)")
        if args.fallback_synced_at is not None:
            age_days = (time.time() - args.fallback_synced_at) / 86400
            report["fallback_age_days"] = round(age_days, 2)
            if age_days > args.max_age_days:
                warnings.append(
                    f"fallback was last synced {age_days:.1f} days ago"
                    f" (> {args.max_age_days:g}); run tools/pricing_fallback_sync.sh sync"
                )
    report["problems"] = problems
    report["blocking"] = blocking
    report["warnings"] = warnings

    for line in render(report):
        print(line)
    for warning in warnings:
        print(f"WARNING: {warning}")
        if args.github:
            print(f"::warning title=Pricing fallback::{warning.splitlines()[0][:300]}")
    if args.json:
        try:
            args.json.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        except OSError as error:
            print(f"INVALID: cannot write report: {error}", file=sys.stderr)
            return EXIT_INVALID
    if problems:
        for problem in problems:
            print(f"INVALID: {problem}", file=sys.stderr)
        return EXIT_INVALID
    if blocking:
        for item in blocking:
            print(f"BLOCK: {item}", file=sys.stderr)
        return EXIT_BLOCK
    print("PASS: no billed or structural pricing problem" if billed is not None
          else "PASS: pricing fallback and remote are valid feeds")
    return EXIT_PASS


if __name__ == "__main__":
    sys.exit(main())
