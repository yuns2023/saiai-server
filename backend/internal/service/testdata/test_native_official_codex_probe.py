"""Offline negative controls for the official-binary preservation comparator."""
import copy
import unittest

from native_official_codex_probe import comparisons


def fixture():
    official = {"kind": "http", "method": "POST", "path": "/backend-api/codex/responses",
                "query": "future=a%2Fb&future=a+b", "sha256": "mock_wire_digest",
                "decoded_sha256": "mock_decoded_digest", "message_type": 0,
                "headers": {"session_id": ["mock_session", "mock_second"],
                            "conversation-id": ["mock_conversation"],
                            "x-future-control": ["first", "second"]}}
    gateway = copy.deepcopy(official)
    gateway.update(stage="gateway", path="/v1/responses")
    provider = copy.deepcopy(official)
    provider.update(stage="provider")
    return [official], [gateway, provider]


class PreservationComparatorTests(unittest.TestCase):
    def test_unchanged_values_and_endpoint_mapping_pass(self):
        captured, receipts = fixture()
        self.assertEqual(comparisons(captured, receipts),
                         {"models": 0, "handshake": 0, "http": 1, "frame": 0})

    def test_session_values_presence_and_multiplicity_are_mandatory(self):
        for replacement in [["namespaced_session", "mock_second"], ["mock_session"],
                            ["mock_second", "mock_session"], None]:
            with self.subTest(replacement=replacement):
                captured, receipts = fixture()
                if replacement is None:
                    del receipts[1]["headers"]["session_id"]
                else:
                    receipts[1]["headers"]["session_id"] = replacement
                with self.assertRaisesRegex(RuntimeError, "header session_id"):
                    comparisons(captured, receipts)

    def test_unknown_control_headers_cannot_be_dropped_or_overwritten(self):
        for replacement in [["second"], ["rewritten"], None]:
            with self.subTest(replacement=replacement):
                captured, receipts = fixture()
                if replacement is None:
                    del receipts[0]["headers"]["x-future-control"]
                else:
                    receipts[1]["headers"]["x-future-control"] = replacement
                with self.assertRaisesRegex(RuntimeError, "header x-future-control"):
                    comparisons(captured, receipts)

    def test_changed_wire_dimensions_fail(self):
        for name, value in [("sha256", "rewritten_wire"), ("decoded_sha256", "rewritten_json"),
                            ("query", "future=a+b&future=a%2Fb"), ("method", "GET"),
                            ("message_type", 2)]:
            with self.subTest(dimension=name):
                captured, receipts = fixture()
                receipts[1][name] = value
                with self.assertRaisesRegex(RuntimeError, name):
                    comparisons(captured, receipts)

    def test_missing_provider_receipt_and_wrong_endpoint_fail(self):
        captured, receipts = fixture()
        with self.assertRaisesRegex(RuntimeError, "http counts"):
            comparisons(captured, receipts[:1])
        receipts[1]["path"] = "/v1/chat/completions"
        with self.assertRaisesRegex(RuntimeError, "endpoint mapping"):
            comparisons(captured, receipts)


if __name__ == "__main__":
    unittest.main()
