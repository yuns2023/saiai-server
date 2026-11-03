#!/usr/bin/env python3
"""Semantic comparison of the bundled pricing fallback against the remote feed.

The bundled LiteLLM snapshot is only the Gateway's cold-start fallback: running
Gateways download pricing.remote_url and refresh it every few minutes. A byte
difference therefore says nothing about billing by itself. This helper reports
what changed in the fields the Gateway parser reads and fails only when:

* either document is not a usable pricing feed (exit 2): not JSON (Go's
  top-level Unmarshal would reject it, e.g. NaN), not an object, fewer than
  --min-priced-models usable entries, or a remote that lost more than
  --max-shrink-ratio of the fallback's usable entries; or
* --billed-keys is given and a billed key is affected (exit 1): removed from
  the remote, missing from the fallback, with different billing fields, or
  ambiguous (a decoded billing field repeated or spelled several ways).

Only fields the Go parser decodes are validated: odd values, overflows or
repeated keys in other metadata never fail. Everything else (added models,
metadata-only drift, unread cost fields, unbilled ambiguous entries) is a
warning. With --fallback-synced-at, drift on a fallback last synced more
than --max-age-days ago adds a staleness warning; it never fails.
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


class JSONObject(dict):
    """A decoded JSON object that remembers keys repeated inside it.

    Go keeps the last value of a repeated key (map or struct field), so the
    dict does too; whether the repetition matters is decided per field.
    """

    duplicates: frozenset[str] = frozenset()


def _track_duplicates(pairs: list[tuple[str, Any]]) -> JSONObject:
    result = JSONObject()
    repeated = set()
    for key, value in pairs:
        if key in result:
            repeated.add(key)
        result[key] = value
    result.duplicates = frozenset(repeated)
    return result


def _reject_constant(value: str) -> Any:
    # NaN/Infinity are not JSON; Go's top-level Unmarshal rejects the whole feed.
    raise FeedError(f"non-JSON constant {value}")


def go_fields(entry: dict[str, Any]) -> tuple[dict[str, Any], str | None]:
    """Map JSON keys to LiteLLMRawEntry fields the way encoding/json matches them.

    Go matches struct tags case-insensitively. When a decoded field appears
    more than once (repeated key or several spellings) Go's result depends on
    decode order, type errors and pointer-vs-value null handling, so the entry
    is reported as ambiguous instead of emulated; so is a non-ASCII key that
    folds onto a known field. Repeats of fields Go ignores do not matter.
    """
    fields: dict[str, Any] = {}
    repeated = getattr(entry, "duplicates", frozenset())
    for key, value in entry.items():
        folded = key.lower() if key.isascii() else key.casefold()
        if folded not in BILLING_FIELDS:
            continue
        if not key.isascii():
            return fields, f"non-ASCII key {key!r} folds onto {folded}"
        if folded in fields or key in repeated:
            return fields, f"field {folded} appears more than once"
        fields[folded] = value
    return fields, None


def _is_number(value: Any) -> bool:
    return isinstance(value, (int, float)) and not isinstance(value, bool)


def billing_view(entry: Any) -> tuple[dict[str, Any] | None, str | None]:
    """Return (Go parser view, ambiguity) for one entry.

    The view is None when Go skips the entry: not an object, a decoded field
    of the wrong type or out of range (a float64 overflow such as 1e400 is an
    Unmarshal error), or neither an input nor an output token price. Go
    copies only non-nil prices into float64 fields, so a missing or null
    price equals an explicit 0, and missing bool/string fields are false/"".
    Fields Go does not decode are never validated.
    """
    if not isinstance(entry, dict):
        return None, None
    fields, ambiguity = go_fields(entry)
    if ambiguity is not None:
        return None, ambiguity
    view: dict[str, Any] = {}
    for field in FLOAT_FIELDS:
        value = fields.get(field)
        if value is None:
            view[field] = None
            continue
        if not _is_number(value):
            return None, None
        try:
            number = float(value)
        except OverflowError:
            return None, None
        if not math.isfinite(number):
            return None, None
        view[field] = number
    for field in INT_FIELDS:
        value = fields.get(field)
        if value is None:
            view[field] = 0
        elif isinstance(value, int) and not isinstance(value, bool) and INT64_MIN <= value <= INT64_MAX:
            view[field] = value
        else:
            return None, None
    for field in BOOL_FIELDS:
        value = fields.get(field)
        if value is None:
            view[field] = False
        elif isinstance(value, bool):
            view[field] = value
        else:
            return None, None
    for field in STRING_FIELDS:
        value = fields.get(field)
        if value is None:
            view[field] = ""
        elif isinstance(value, str):
            view[field] = value
        else:
            return None, None
    if view["input_cost_per_token"] is None and view["output_cost_per_token"] is None:
        return None, None
    for field in FLOAT_FIELDS:
        if view[field] is None:
            view[field] = 0.0
    return view, None


def load_feed(text: str, label: str) -> dict[str, Any]:
    try:
        document = json.loads(
            text, object_pairs_hook=_track_duplicates, parse_constant=_reject_constant,
        )
    except FeedError as error:
        raise FeedError(f"{label}: {error}") from None
    except ValueError as error:
        raise FeedError(f"{label}: not valid JSON ({error})") from None
    if not isinstance(document, dict):
        raise FeedError(f"{label}: top level is not a JSON object")
    return document


def priced_views(document: dict[str, Any]) -> tuple[dict[str, dict[str, Any]], dict[str, str]]:
    views: dict[str, dict[str, Any]] = {}
    ambiguous: dict[str, str] = {}
    for name, entry in document.items():
        if name in DOC_KEYS:
            continue
        view, ambiguity = billing_view(entry)
        if ambiguity is not None:
            ambiguous[name] = ambiguity
        elif view is not None:
            views[name] = view
    return views, ambiguous


def unread_cost_fields(entry: Any) -> dict[str, Any]:
    if not isinstance(entry, dict):
        return {}
    return {
        key: value
        for key, value in entry.items()
        if "cost" in key.lower() and key.lower() not in BILLING_FIELDS
    }


def compare(fallback: dict[str, Any], remote: dict[str, Any]) -> dict[str, Any]:
    fallback_views, fallback_ambiguous = priced_views(fallback)
    remote_views, remote_ambiguous = priced_views(remote)
    ambiguous = set(fallback_ambiguous) | set(remote_ambiguous)
    added = sorted(set(remote_views) - set(fallback_views) - ambiguous)
    removed = sorted(set(fallback_views) - set(remote_views) - ambiguous)
    billing_changed: dict[str, list[str]] = {}
    unread_cost_changed: list[str] = []
    metadata_only: list[str] = []
    for name in sorted(set(fallback_views) & set(remote_views)):
        old_view, new_view = fallback_views[name], remote_views[name]
        fields = [field for field in BILLING_FIELDS if old_view[field] != new_view[field]]
        unread = unread_cost_fields(fallback[name]) != unread_cost_fields(remote[name])
        if fields:
            billing_changed[name] = fields
        if unread:
            unread_cost_changed.append(name)
        if not fields and not unread and fallback[name] != remote[name]:
            metadata_only.append(name)
    unpriced_changed = sorted(
        name
        for name in set(fallback) | set(remote)
        if name not in fallback_views
        and name not in remote_views
        and name not in ambiguous
        and fallback.get(name) != remote.get(name)
    )
    repeated_models = sorted(
        set(getattr(fallback, "duplicates", ())) | set(getattr(remote, "duplicates", ()))
    )
    return {
        "fallback_priced_models": len(fallback_views),
        "remote_priced_models": len(remote_views),
        "added": added,
        "removed": removed,
        "billing_changed": billing_changed,
        "unread_cost_changed": unread_cost_changed,
        "ambiguous": {
            name: (remote_ambiguous.get(name) or fallback_ambiguous[name])
            for name in sorted(ambiguous)
        },
        "repeated_model_keys": repeated_models,
        "metadata_only_changed": len(metadata_only),
        "unpriced_or_doc_changed": len(unpriced_changed),
        # Equal final values can still differ in repeated/ambiguous fields.
        "drift": (fallback != remote or fallback_ambiguous != remote_ambiguous
                  or getattr(fallback, "duplicates", None) != getattr(remote, "duplicates", None)),
        "fallback_ambiguous_models": len(fallback_ambiguous),
        "remote_ambiguous_models": len(remote_ambiguous),
        "_views": (fallback_views, remote_views),
    }


def billed_impact(report: dict[str, Any], billed: set[str]) -> dict[str, list[str]]:
    fallback_views, remote_views = report["_views"]
    ambiguous = set(report["ambiguous"])
    return {
        "billed_removed": sorted(billed & set(report["removed"])),
        "billed_missing_from_fallback": sorted(
            key for key in billed - ambiguous if key in remote_views and key not in fallback_views),
        "billed_cost_changed": sorted(billed & set(report["billing_changed"])),
        "billed_ambiguous": sorted(billed & ambiguous),
        "billed_in_neither": sorted(
            key for key in billed - ambiguous if key not in remote_views and key not in fallback_views),
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
    if not (math.isfinite(args.max_age_days) and args.max_age_days > 0):
        parser.error("--max-age-days must be a finite positive number")

    try:
        fallback = load_feed(args.fallback.read_text(encoding="utf-8"), "fallback")
        remote = load_feed(args.remote.read_text(encoding="utf-8"), "remote")
        report = compare(fallback, remote)
        views = report.pop("_views")
        billed = read_billed_keys(args.billed_keys) if args.billed_keys else None
    except (FeedError, OSError, UnicodeDecodeError) as error:
        print(f"INVALID: {error}", file=sys.stderr)
        return EXIT_INVALID

    problems: list[str] = []
    for label in ("fallback", "remote"):
        count = report[f"{label}_priced_models"]
        # Ambiguous entries are uncertain, so they never count as usable models.
        if count < args.min_priced_models:
            problems.append(
                f"{label} has {count} usable priced models (< {args.min_priced_models}; "
                f"{report[f'{label}_ambiguous_models']} ambiguous excluded)"
            )
    floor = report["fallback_priced_models"] * (1 - args.max_shrink_ratio)
    if report["remote_priced_models"] < floor:
        problems.append(
            f"remote lost more than {args.max_shrink_ratio:.0%} of the fallback's priced models "
            f"({report['remote_ambiguous_models']} ambiguous excluded)"
        )

    warnings: list[str] = []
    blocking: list[str] = []
    if billed is not None:
        impact = billed_impact({**report, "_views": views}, billed)
        report["billed_keys"] = len(billed)
        report.update(impact)
        for field, text in (
            ("billed_removed", "billed key removed from remote"),
            ("billed_missing_from_fallback", "billed key missing from fallback"),
            ("billed_cost_changed", "billed key billing fields changed"),
            ("billed_ambiguous", "billed key has an ambiguous billing field"),
        ):
            if impact[field]:
                blocking.append(f"{text}: " + ", ".join(safe(key) for key in impact[field]))
        if impact["billed_in_neither"]:
            warnings.append(
                "billed key in neither feed (alias/override or retired; review): "
                + ", ".join(safe(key) for key in impact["billed_in_neither"])
            )
    unbilled_ambiguous = [name for name in report["ambiguous"] if billed is None or name not in billed]
    if unbilled_ambiguous:
        warnings.append(
            "entries ignored as ambiguous (a decoded billing field repeated or spelled several ways): "
            + ", ".join(safe(name) for name in unbilled_ambiguous[:20])
            + (f", ... (+{len(unbilled_ambiguous) - 20})" if len(unbilled_ambiguous) > 20 else "")
        )
    if report["repeated_model_keys"]:
        warnings.append(
            "model keys repeated at top level (last one wins, as in Go): "
            + ", ".join(safe(name) for name in report["repeated_model_keys"][:20])
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
