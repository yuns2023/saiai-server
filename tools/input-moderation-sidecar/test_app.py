import unittest
from unittest.mock import patch

import app


class FakeTokenizer:
    def encode(self, text, add_special_tokens=False):
        del add_special_tokens
        return list(range(len(text)))

    def decode(self, token_ids, skip_special_tokens=True):
        del skip_special_tokens
        return ",".join(str(token_id) for token_id in token_ids)


class InputModerationTests(unittest.TestCase):
    def test_parse_model_output_normalizes_and_deduplicates_categories(self):
        safety, categories = app.parse_model_output(
            "Safety: unsafe\nCategories: PII, Jailbreak, pii"
        )
        self.assertEqual("Unsafe", safety)
        self.assertEqual(["PII", "Jailbreak"], categories)

    def test_parse_model_output_rejects_missing_safety_label(self):
        with self.assertRaisesRegex(ValueError, "omitted Safety label"):
            app.parse_model_output("Categories: None")

    def test_split_text_applies_overlap_and_chunk_limit(self):
        with patch.object(app, "tokenizer", FakeTokenizer()), patch.object(
            app, "MAX_CHUNK_TOKENS", 256
        ), patch.object(app, "CHUNK_OVERLAP_TOKENS", 16), patch.object(
            app, "MAX_CHUNKS", 2
        ):
            chunks = app.split_text("x" * 600)
        self.assertEqual(2, len(chunks))
        self.assertTrue(chunks[0].startswith("0,1,2"))
        self.assertTrue(chunks[1].startswith("240,241,242"))

    def test_classify_keeps_most_severe_result_and_category_union(self):
        with patch.object(app, "split_text", return_value=["a", "b"]), patch.object(
            app,
            "classify_chunk",
            side_effect=[("Controversial", ["PII"]), ("Unsafe", ["PII", "Jailbreak"])],
        ):
            result = app.classify("test")
        self.assertEqual("Unsafe", result.safety)
        self.assertEqual(["PII", "Jailbreak"], result.categories)
        self.assertEqual(app.MODEL_VERSION, result.model_version)


if __name__ == "__main__":
    unittest.main()
