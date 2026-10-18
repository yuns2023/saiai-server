import json
from pathlib import Path
import tempfile
import unittest
from validate import BOUNDARY, FIXTURE, validate, validate_suite


class FixtureValidationTests(unittest.TestCase):
    def test_manifest_does_not_claim_independent_trials_or_model_calls(self):
        result = validate()
        self.assertEqual(result['model_calls'], 0)
        self.assertEqual(result['holdout_samples'], 0)
        self.assertEqual(result['samples'], 76)
        self.assertEqual(result['scenario_families'], 38)

    def test_invalid_labels_duplicates_and_false_qualification_are_rejected(self):
        original = [json.loads(line) for line in FIXTURE.read_text().splitlines()]
        mutations = [
            lambda rows: rows[0].update(positive_categories=['unknown']),
            lambda rows: rows[1].update(id=rows[0]['id']),
            lambda rows: rows[0].update(negative_categories=rows[0]['positive_categories']),
            lambda rows: rows[0].update(split='holdout'),
            lambda rows: rows[0].update(review_status='independently_reviewed'),
            lambda rows: rows[1].update(text=rows[0]['text'].upper()),
            lambda rows: rows[1].update(positive_categories=[] , negative_categories=['Jailbreak']),
            lambda rows: rows[0].update(positive_categories=['Jailbreak', 'Jailbreak']),
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as folder:
                rows = json.loads(json.dumps(original))
                mutation(rows)
                path = Path(folder) / 'fixture.jsonl'
                path.write_text(''.join(json.dumps(row) + '\n' for row in rows))
                with self.assertRaises(ValueError):
                    validate(fixture_path=path)

    def test_suite_is_only_a_candidate(self):
        result = validate_suite()
        self.assertEqual(result['samples'], 108)
        self.assertEqual(result['scenario_families'], 54)
        self.assertEqual(result['holdout_candidate_samples'], 32)
        self.assertEqual(result['holdout_samples'], 0)
        self.assertEqual(result['independently_reviewed_samples'], 0)
        self.assertEqual(result['model_calls'], 0)

    def test_cross_split_leakage_is_rejected_even_with_new_ids(self):
        original = [json.loads(line) for line in BOUNDARY.read_text().splitlines()]
        calibration = json.loads(FIXTURE.read_text().splitlines()[0])
        for field, value in [('id', calibration['id']),
                             ('family_id', calibration['family_id']),
                             ('text', '  ' + calibration['text'].upper() + '  ')]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as folder:
                rows = json.loads(json.dumps(original))
                rows[0][field] = value
                path = Path(folder) / 'boundary.jsonl'
                path.write_text(''.join(json.dumps(row) + '\n' for row in rows))
                with self.assertRaisesRegex(ValueError, 'cross-split'):
                    validate_suite(boundary=path)


if __name__ == '__main__':
    unittest.main()
