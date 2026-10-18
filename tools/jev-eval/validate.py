#!/usr/bin/env python3
"""Offline-only fixture validator. No network, credentials or model invocation."""
import argparse
import hashlib
import json
from pathlib import Path
from collections import Counter
import unicodedata

ROOT = Path(__file__).resolve().parents[2]
REGISTRY = ROOT / "backend/internal/service/jev_category_registry.json"
FIXTURE = Path(__file__).resolve().parent / "fixtures/expanded-calibration-draft.jsonl"
BOUNDARY = FIXTURE.with_name("boundary-holdout-candidate.jsonl")

def normalized_text(text):
    return " ".join(unicodedata.normalize("NFKC", text).casefold().split())


def validate(registry_path=REGISTRY, fixture_path=FIXTURE, *, split="calibration_draft",
             require_coverage=True):
    if split not in {"calibration_draft", "holdout_candidate"}:
        raise ValueError("unsupported draft split")
    categories = json.loads(registry_path.read_text())
    ids = {c["id"] for c in categories}
    if len(ids) != len(categories):
        raise ValueError("duplicate registry id")
    rows = [json.loads(line) for line in fixture_path.read_text().splitlines() if line.strip()]
    seen = set()
    families = {}
    coverage = Counter()
    texts = set()
    family_labels = {}
    for row in rows:
        if row["id"] in seen or not row["text"].strip():
            raise ValueError("duplicate id or empty text")
        seen.add(row["id"])
        if row["split"] != split or row["review_status"] != "unreviewed":
            raise ValueError("draft cannot claim independent review or holdout qualification")
        text = normalized_text(row["text"])
        if text in texts:
            raise ValueError("duplicate normalized text")
        texts.add(text)
        if row["family_id"] in families and families[row["family_id"]] != row["split"]:
            raise ValueError("scenario family split leakage")
        families[row["family_id"]] = row["split"]
        positive = set(row["positive_categories"])
        negative = set(row["negative_categories"])
        if len(positive) != len(row["positive_categories"]) or len(negative) != len(row["negative_categories"]):
            raise ValueError("duplicate category label")
        if positive & negative or not (positive | negative) or not (positive | negative) <= ids:
            raise ValueError("invalid labels")
        if row["language"] not in {"zh", "en"}:
            raise ValueError("unsupported draft language")
        labels = (positive, negative)
        if row["family_id"] in family_labels and family_labels[row["family_id"]] != labels:
            raise ValueError("translation label mismatch")
        family_labels[row["family_id"]] = labels
        for category in positive:
            coverage[(category, "positive", row["language"])] += 1
        for category in negative:
            coverage[(category, "negative", row["language"])] += 1
    for category in ids if require_coverage else ():
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


def validate_suite(calibration=FIXTURE, boundary=BOUNDARY):
    reports = [validate(fixture_path=calibration), validate(
        fixture_path=boundary, split="holdout_candidate", require_coverage=False)]
    ids, families, texts = set(), set(), set()
    for path in (calibration, boundary):
        rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
        current_ids = {r["id"] for r in rows}
        current_families = {r["family_id"] for r in rows}
        current_texts = {normalized_text(r["text"]) for r in rows}
        if ids & current_ids or families & current_families or texts & current_texts:
            raise ValueError("cross-split id, family or normalized text leakage")
        ids.update(current_ids)
        families.update(current_families)
        texts.update(current_texts)
    return {"offline_only": True, "model_calls": 0, "samples": len(ids),
            "scenario_families": len(families), "independently_reviewed_samples": 0,
            "holdout_samples": 0, "holdout_candidate_samples": reports[1]["samples"],
            "potential_single_pass_calls": len(ids), "datasets": reports}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--fixture", type=Path, default=FIXTURE)
    parser.add_argument("--suite", action="store_true", help="validate both drafts and split leakage")
    args = parser.parse_args()
    print(json.dumps(validate_suite() if args.suite else validate(fixture_path=args.fixture), ensure_ascii=False, indent=2))
