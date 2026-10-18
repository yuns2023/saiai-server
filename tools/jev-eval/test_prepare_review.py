import csv
import json
from pathlib import Path
import tempfile
import unittest
from prepare_review import prepare


class ReviewPacketTests(unittest.TestCase):
    def test_packet_hides_author_labels_and_tracks_all_questions(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / 'packet'
            plan = prepare(output)
            with (output / 'annotations.csv').open() as f:
                rows = list(csv.DictReader(f))
            self.assertEqual(len(rows), 108 * 19)
            self.assertEqual(len({r['sample_id'] for r in rows}), 108)
            self.assertTrue(all(not r['label'] and not r['rationale'] and not r['reviewer'] for r in rows))
            self.assertTrue(all('positive' not in r['sample_id'] and 'negative' not in r['sample_id'] for r in rows))
            self.assertEqual(plan['model_calls'], 0)
            self.assertFalse(plan['authorized'])
            self.assertIsNone(plan['token_count'])
            self.assertIsNone(plan['enforced_monetary_cap_usd'])
            self.assertEqual(len(plan['request_sha256']), 108)
            with self.assertRaises(FileExistsError):
                prepare(output)

    def test_preparation_is_reproducible(self):
        with tempfile.TemporaryDirectory() as folder:
            first = prepare(Path(folder) / 'first')
            second = prepare(Path(folder) / 'second')
            self.assertEqual(first, second)
            for name in ('annotations.csv', 'coordinator-map.json', 'request-plan.json'):
                self.assertEqual((Path(folder) / 'first' / name).read_bytes(),
                                 (Path(folder) / 'second' / name).read_bytes())


if __name__ == '__main__':
    unittest.main()
