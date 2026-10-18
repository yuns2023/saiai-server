#!/usr/bin/env python3
"""Validate the post-observation language calibration draft; no network or keys."""
import json
from pathlib import Path
from validate import BOUNDARY, FIXTURE, normalized_text, validate

FOLLOWUP = FIXTURE.with_name('language-followup-calibration.jsonl')


def validate_followup(path=FOLLOWUP):
    report = validate(fixture_path=path, require_coverage=False)
    original = [json.loads(line) for p in (FIXTURE, BOUNDARY) for line in p.read_text().splitlines()]
    prior_ids = {r['id'] for r in original}
    prior_families = {r['family_id'] for r in original}
    prior_texts = {normalized_text(r['text']) for r in original}
    rows = [json.loads(line) for line in path.read_text().splitlines()]
    families = {}
    contrasts = set()
    for row in rows:
        if row['id'] in prior_ids or row['family_id'] in prior_families or normalized_text(row['text']) in prior_texts:
            raise ValueError('duplicate prior sample or family')
        if row.get('source') != 'authored_after_expanded_108_v1' or row.get('independent_acceptance') is not False:
            raise ValueError('follow-up cannot claim independence')
        if not row.get('contrast_group') or not row.get('label_rationale'):
            raise ValueError('contrast and rationale required')
        family = families.setdefault(row['family_id'], {'languages': set(), 'contrast': row['contrast_group']})
        if row['language'] in family['languages'] or family['contrast'] != row['contrast_group']:
            raise ValueError('duplicate translation or contrast mismatch')
        family['languages'].add(row['language'])
        contrasts.add(row['contrast_group'])
    if any(f['languages'] != {'en', 'zh'} for f in families.values()):
        raise ValueError('missing paired translation')
    report.update(independent_acceptance=False, contrast_groups=len(contrasts),
                  prior_model_exposure='authored after observing the 108-call results',
                  runner_supported=True, live_calls_authorized=False)
    return report


if __name__ == '__main__':
    print(json.dumps(validate_followup(), ensure_ascii=False, indent=2))
