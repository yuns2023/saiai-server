import hashlib
import json
import unittest
from prepare_review import MODEL
from report import summarize
from validate import FIXTURE, REGISTRY, validate_suite


def document():
    return {'origin': 'mock', 'model': MODEL,
            'fixture_sha256': [d['fixture_sha256'] for d in validate_suite()['datasets']],
            'registry_sha256': hashlib.sha256(REGISTRY.read_bytes()).hexdigest(), 'results': []}


def scores(value):
    return {c['id']: value for c in json.loads(REGISTRY.read_text())}


class ReportTests(unittest.TestCase):
    def test_failed_missing_and_unknown_are_not_safe_negatives(self):
        data = document()
        first = json.loads(FIXTURE.read_text().splitlines()[0])
        data['results'] = [{'sample_id': first['id'], 'status': 'failed'}]
        report = summarize(data)
        self.assertEqual(report['failed_samples'], 1)
        self.assertEqual(report['missing_samples'], 107)
        self.assertEqual(report['categories']['Jailbreak']['tn'], 0)
        self.assertGreater(report['categories']['Jailbreak']['unavailable_positive'], 0)
        self.assertGreater(report['categories']['PII']['unknown_labels'], 0)
        self.assertIsNone(report['categories']['Jailbreak']['recall_on_completed'])
        self.assertFalse(report['independent_acceptance'])

    def test_inclusive_threshold_and_related_family_counts(self):
        data = document()
        for line in FIXTURE.read_text().splitlines()[:2]:
            row = json.loads(line)
            data['results'].append({'sample_id': row['id'], 'status': 'ok', 'scores': scores(.9)})
        report = summarize(data)
        self.assertEqual(report['categories']['Jailbreak']['tp'], 2)
        self.assertEqual(report['categories']['Jailbreak']['completed_labeled_families'], 1)
        self.assertEqual(report['categories']['PII']['fp'], 0)
        self.assertFalse(report['enforcement_eligible'])

    def test_reject_invalid_coordinates_duplicates_and_scores(self):
        row = json.loads(FIXTURE.read_text().splitlines()[0])
        mutations = [
            lambda d: d.update(model='other'),
            lambda d: d.update(registry_sha256='stale'),
            lambda d: d.update(fixture_sha256=[]),
            lambda d: d['results'].append(d['results'][0]),
            lambda d: d['results'][0].update(sample_id='unknown'),
            lambda d: d['results'][0].update(scores={}),
            lambda d: d['results'][0]['scores'].update(PII=True),
            lambda d: d['results'][0]['scores'].update(PII=float('nan')),
            lambda d: d['results'][0]['scores'].update(PII=1.1),
            lambda d: d['results'][0].update(status='failed'),
        ]
        for change in mutations:
            data = document()
            data['results'] = [{'sample_id': row['id'], 'status': 'ok', 'scores': scores(.5)}]
            change(data)
            with self.assertRaises(ValueError):
                summarize(data)

    def test_language_slices_expose_translation_difference(self):
        data = document()
        rows = [json.loads(line) for line in FIXTURE.read_text().splitlines()[:2]]
        for row in rows:
            value = .95 if row['language'] == 'zh' else .55
            data['results'].append({'sample_id': row['id'], 'status': 'ok', 'scores': scores(value)})
        report = summarize(data)
        self.assertEqual(report['by_language']['zh']['Jailbreak']['tp'], 1)
        self.assertEqual(report['by_language']['en']['Jailbreak']['fn'], 1)
