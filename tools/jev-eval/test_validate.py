import json
from pathlib import Path
import tempfile
import unittest
from validate import FIXTURE, validate


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
        ]
        for mutation in mutations:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as folder:
                rows = json.loads(json.dumps(original))
                mutation(rows)
                path = Path(folder) / 'fixture.jsonl'
                path.write_text(''.join(json.dumps(row) + '\n' for row in rows))
                with self.assertRaises(ValueError):
                    validate(fixture_path=path)


if __name__ == '__main__':
    unittest.main()
