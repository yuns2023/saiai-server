import unittest
from pathlib import Path
from tempfile import TemporaryDirectory
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
    def test_checkpoint_layout_rejects_symlinked_model_directory(self):
        with TemporaryDirectory() as temp_dir:
            root = Path(temp_dir)
            target = root / "target"
            target.mkdir()
            (target / "model.safetensors").write_bytes(b"test-only")
            model_path = root / "model"
            model_path.symlink_to(target, target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError, "model directory is missing"):
                app.validate_model_checkpoint_layout(model_path)

    def test_checkpoint_layout_accepts_single_regular_safetensors_file(self):
        with TemporaryDirectory() as temp_dir:
            model_path = Path(temp_dir)
            (model_path / "model.safetensors").write_bytes(b"test-only")
            app.validate_model_checkpoint_layout(model_path)

    def test_checkpoint_layout_rejects_sharded_index(self):
        with TemporaryDirectory() as temp_dir:
            model_path = Path(temp_dir)
            (model_path / "model.safetensors").write_bytes(b"test-only")
            (model_path / "model.safetensors.index.json").write_text(
                '{"weight_map":{"model.weight":"../outside.safetensors"}}',
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "sharded checkpoint indexes"):
                app.validate_model_checkpoint_layout(model_path)

    def test_checkpoint_layout_rejects_symlinked_weights(self):
        with TemporaryDirectory() as temp_dir:
            model_path = Path(temp_dir)
            target = model_path / "target.safetensors"
            target.write_bytes(b"test-only")
            (model_path / "model.safetensors").symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, "regular model.safetensors"):
                app.validate_model_checkpoint_layout(model_path)

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
