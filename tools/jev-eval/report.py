#!/usr/bin/env python3
"""Summarize supplied evaluation results offline; never invoke a model."""
import argparse
import hashlib
import json
import math
from pathlib import Path
from prepare_review import MODEL
from validate import FIXTURE, BOUNDARY, REGISTRY, validate_suite


def summarize(document, threshold=0.9):
    if isinstance(threshold, bool) or not isinstance(threshold, (int, float)) or not math.isfinite(threshold) or not 0 < threshold <= 1:
        raise ValueError('invalid threshold')
    suite = validate_suite()
    expected = [d['fixture_sha256'] for d in suite['datasets']]
    if (document.get('fixture_sha256') != expected or
            document.get('registry_sha256') != hashlib.sha256(REGISTRY.read_bytes()).hexdigest() or
            document.get('model') != MODEL):
        raise ValueError('dataset, rules or model mismatch')
    if document.get('origin') not in {'mock', 'provider'}:
        raise ValueError('result origin must be explicit')
    samples = [json.loads(line) for p in (FIXTURE, BOUNDARY) for line in p.read_text().splitlines() if line.strip()]
    by_id = {r['id']: r for r in samples}
    categories = [c['id'] for c in json.loads(REGISTRY.read_text())]
    observations = {}
    failures = 0
    for observation in document['results']:
        sample_id = observation['sample_id']
        if sample_id not in by_id or sample_id in observations:
            raise ValueError('unknown or duplicate sample id')
        if observation.get('status') not in {'ok', 'failed'}:
            raise ValueError('invalid result status')
        if observation['status'] == 'ok':
            scores = observation.get('scores', {})
            if set(scores) != set(categories) or any(
                isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or not 0 <= v <= 1
                for v in scores.values()
            ):
                raise ValueError('incomplete or invalid scores')
        else:
            failures += 1
            if observation.get('scores'):
                raise ValueError('failed request cannot contain usable scores')
        observations[sample_id] = observation
    metrics = {}
    for category in categories:
        counts = dict(tp=0, tn=0, fp=0, fn=0, unknown_labels=0, unavailable_positive=0, unavailable_negative=0)
        families = set()
        for row in samples:
            positive = category in row['positive_categories']
            negative = category in row['negative_categories']
            if not positive and not negative:
                counts['unknown_labels'] += 1
                continue
            result = observations.get(row['id'])
            if result is None or result['status'] != 'ok':
                counts['unavailable_positive' if positive else 'unavailable_negative'] += 1
                continue
            families.add(row['family_id'])
            detected = result['scores'][category] >= threshold
            counts[('tp' if detected else 'fn') if positive else ('fp' if detected else 'tn')] += 1
        def ratio(n, d):
            return n / d if d else None
        counts.update(
            precision_on_completed=ratio(counts['tp'], counts['tp'] + counts['fp']),
            recall_on_completed=ratio(counts['tp'], counts['tp'] + counts['fn']),
            false_positive_rate_on_completed=ratio(counts['fp'], counts['fp'] + counts['tn']),
            positive_detection_coverage=ratio(counts['tp'], counts['tp'] + counts['fn'] + counts['unavailable_positive']),
            completed_labeled_families=len(families),
        )
        metrics[category] = counts
    return dict(
        offline_analysis=True, origin=document['origin'], label_basis='unreviewed authored drafts',
        independent_acceptance=False, enforcement_eligible=False, threshold=threshold,
        expected_samples=len(samples), supplied_results=len(observations),
        completed_samples=len(observations) - failures, failed_samples=failures,
        missing_samples=len(samples) - len(observations), scenario_families=suite['scenario_families'],
        confidence_intervals=None,
        limitation='Translations are related. No independent-family confidence interval or production accuracy claim.',
        cost_estimate=None, cost_limitation='This report does not account for usage; missing/failed calls are not assumed free.',
        categories=metrics,
    )


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--results', type=Path, required=True)
    parser.add_argument('--threshold', type=float, default=0.9)
    args = parser.parse_args()
    try:
        report = summarize(json.loads(args.results.read_text()), args.threshold)
    except (ValueError, TypeError, KeyError):
        parser.exit(2, 'Invalid result document; no report produced.\n')
    print(json.dumps(report, ensure_ascii=False, indent=2))
