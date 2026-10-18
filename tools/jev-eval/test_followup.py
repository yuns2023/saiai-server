import json
from pathlib import Path
import tempfile
import unittest
from followup import FOLLOWUP, validate_followup
from validate import FIXTURE


class FollowupTests(unittest.TestCase):
    def test_language_pairs_are_calibration_not_independent_trials(self):
        report = validate_followup()
        self.assertEqual(report['samples'], 32)
        self.assertEqual(report['scenario_families'], 16)
        self.assertEqual(report['contrast_groups'], 6)
        self.assertFalse(report['independent_acceptance'])
        self.assertFalse(report['live_calls_authorized'])
        self.assertFalse(report['runner_supported'])

    def test_false_independence_leakage_and_missing_translation_rejected(self):
        rows = [json.loads(line) for line in FOLLOWUP.read_text().splitlines()]
        old = json.loads(FIXTURE.read_text().splitlines()[0])
        mutations = [lambda rs: rs[0].update(independent_acceptance=True),
                     lambda rs: rs[0].update(text=old['text']),
                     lambda rs: rs[0].update(family_id=old['family_id']),
                     lambda rs: rs.pop(),
                     lambda rs: rs[1].update(language='zh'),
                     lambda rs: rs[1].update(contrast_group='other')]
        for mutation in mutations:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as folder:
                changed = json.loads(json.dumps(rows))
                mutation(changed)
                path = Path(folder) / 'fixture.jsonl'
                path.write_text(''.join(json.dumps(r) + '\n' for r in changed))
                with self.assertRaises(ValueError):
                    validate_followup(path)
