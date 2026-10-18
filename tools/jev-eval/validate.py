#!/usr/bin/env python3
"""Offline-only fixture validator. No network, credentials or model invocation."""
import argparse
import hashlib
import json
from pathlib import Path
from collections import Counter

ROOT = Path(__file__).resolve().parents[2]
REGISTRY = ROOT / "backend/internal/service/jev_category_registry.json"
FIXTURE = Path(__file__).resolve().parent / "fixtures/expanded-calibration-draft.jsonl"

def validate(registry_path=REGISTRY, fixture_path=FIXTURE):
    categories = json.loads(registry_path.read_text())
    ids = {c["id"] for c in categories}
    if len(ids) != len(categories):
        raise ValueError("duplicate registry id")
    rows = [json.loads(line) for line in fixture_path.read_text().splitlines() if line.strip()]
    seen = set()
    families = {}
    coverage = Counter()
    for row in rows:
        if row["id"] in seen or not row["text"].strip():
            raise ValueError("duplicate id or empty text")
        seen.add(row["id"])
        if row["split"] != "calibration_draft" or row["review_status"] != "unreviewed":
            raise ValueError("draft cannot claim independent review or holdout qualification")
        if row["family_id"] in families and families[row["family_id"]] != row["split"]:
            raise ValueError("scenario family split leakage")
        families[row["family_id"]] = row["split"]
        positive = set(row["positive_categories"])
        negative = set(row["negative_categories"])
        if positive & negative or not (positive | negative) or not (positive | negative) <= ids:
            raise ValueError("invalid labels")
        if row["language"] not in {"zh", "en"}:
            raise ValueError("unsupported draft language")
        for category in positive:
            coverage[(category, "positive", row["language"])] += 1
        for category in negative:
            coverage[(category, "negative", row["language"])] += 1
    for category in ids:
        for polarity in ("positive", "negative"):
            for language in ("zh", "en"):
                if not coverage[(category, polarity, language)]:
                    raise ValueError("missing category/polarity/language coverage")
    return {
        "offline_only": True,
        "model_calls": 0,
        "categories": len(ids),
        "samples": len(rows),
        "scenario_families": len(families),
        "independently_reviewed_samples": 0,
        "holdout_samples": 0,
        "potential_single_pass_calls": len(rows),
        "text_characters": sum(len(row["text"]) for row in rows),
        "registry_sha256": hashlib.sha256(registry_path.read_bytes()).hexdigest(),
        "fixture_sha256": hashlib.sha256(fixture_path.read_bytes()).hexdigest(),
    }

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture", type=Path, default=FIXTURE)
    args = parser.parse_args()
    print(json.dumps(validate(fixture_path=args.fixture), ensure_ascii=False, indent=2))
