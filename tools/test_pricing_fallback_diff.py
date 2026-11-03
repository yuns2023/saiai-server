#!/usr/bin/env python3
"""Exercise the semantic pricing comparison without network access."""

from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HELPER = ROOT / "tools" / "pricing_fallback_diff.py"
SYNC = ROOT / "tools" / "pricing_fallback_sync.sh"

spec = importlib.util.spec_from_file_location("pricing_fallback_diff", HELPER)
diff = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(diff)


def feed(count: int = 3, **overrides: object) -> dict[str, object]:
    document: dict[str, object] = {
        "sample_spec": {"input_cost_per_token": 0, "max_tokens": "doc"},
    }
    for index in range(count):
        document[f"model-{index}"] = {
            "input_cost_per_token": 1e-6,
            "output_cost_per_token": 2e-6,
            "litellm_provider": "openai",
            "mode": "chat",
            "max_input_tokens": 1000,
        }
    for name, value in overrides.items():
        if value is None:
            document.pop(name, None)
        else:
            document[name] = value
    return document


class PricingDiffTests(unittest.TestCase):
    def run_main(self, fallback: object, remote: object, *extra: str,
                 billed: list[str] | None = None, raw: tuple[str, str] | None = None
                 ) -> tuple[int, str, str, dict]:
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            fallback_path, remote_path = base / "fallback.json", base / "remote.json"
            if raw is not None:
                fallback_path.write_text(raw[0])
                remote_path.write_text(raw[1])
            else:
                fallback_path.write_text(json.dumps(fallback))
                remote_path.write_text(json.dumps(remote))
            arguments = ["--fallback", str(fallback_path), "--remote", str(remote_path),
                         "--min-priced-models", "1", "--json", str(base / "report.json"), *extra]
            if billed is not None:
                (base / "billed.txt").write_text("\n".join(billed) + "\n")
                arguments += ["--billed-keys", str(base / "billed.txt")]
            stdout, stderr = io.StringIO(), io.StringIO()
            with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = diff.main(arguments)
            report_path = base / "report.json"
            report = json.loads(report_path.read_text()) if report_path.exists() else {}
            return code, stdout.getvalue(), stderr.getvalue(), report

    def test_identical_feeds_pass_without_drift(self) -> None:
        code, out, _, report = self.run_main(feed(), feed())
        self.assertEqual(code, 0)
        self.assertFalse(report["drift"])
        self.assertIn("PASS", out)

    def test_metadata_only_drift_passes_with_warning(self) -> None:
        remote = feed()
        remote["model-1"] = {**remote["model-1"], "max_input_tokens": 2000, "supports_vision": True}
        code, out, _, report = self.run_main(feed(), remote)
        self.assertEqual(code, 0)
        self.assertEqual(report["metadata_only_changed"], 1)
        self.assertEqual(report["billing_changed"], {})
        self.assertIn("WARNING: fallback differs from remote", out)

    def test_number_spelling_is_not_a_price_change(self) -> None:
        raw_fallback = json.dumps(feed()).replace("1e-06", "0.000001")
        code, _, _, report = self.run_main(None, None, raw=(raw_fallback, json.dumps(feed())))
        self.assertEqual(code, 0)
        self.assertEqual(report["billing_changed"], {})

    def test_unbilled_cost_change_and_removal_pass_without_billed_keys(self) -> None:
        remote = feed(10, **{"model-2": None})
        remote["model-0"] = {**remote["model-0"], "output_cost_per_token": 3e-6}
        remote["model-new"] = dict(remote["model-1"])
        code, out, _, report = self.run_main(feed(10), remote)
        self.assertEqual(code, 0)
        self.assertEqual(report["removed"], ["model-2"])
        self.assertEqual(report["added"], ["model-new"])
        self.assertEqual(report["billing_changed"], {"model-0": ["output_cost_per_token"]})
        self.assertIn('billing changed: "model-0"', out)

    def test_billed_keys_block_on_change_removal_and_missing_fallback(self) -> None:
        remote = feed(10, **{"model-2": None})
        remote["model-0"] = {**remote["model-0"], "cache_read_input_token_cost": 1e-7}
        remote["model-new"] = dict(remote["model-1"])
        code, _, err, report = self.run_main(
            feed(10), remote, billed=["model-0", "model-1", "model-2", "model-new", "alias-x"])
        self.assertEqual(code, 1)
        self.assertEqual(report["billed_cost_changed"], ["model-0"])
        self.assertEqual(report["billed_removed"], ["model-2"])
        self.assertEqual(report["billed_missing_from_fallback"], ["model-new"])
        self.assertEqual(report["billed_in_neither"], ["alias-x"])
        self.assertIn("BLOCK:", err)

    def test_billed_keys_untouched_pass(self) -> None:
        remote = feed(10, **{"model-2": None})
        code, out, _, report = self.run_main(feed(10), remote, billed=["model-0", "model-1"])
        self.assertEqual(code, 0)
        self.assertEqual(report["billed_keys"], 2)
        self.assertIn("PASS: no billed", out)

    def test_billing_flags_count_as_billing_fields(self) -> None:
        remote = feed()
        remote["model-0"] = {**remote["model-0"], "supports_service_tier": True}
        remote["model-1"] = {**remote["model-1"], "long_context_input_token_threshold": 200000}
        _, _, _, report = self.run_main(feed(), remote)
        self.assertEqual(sorted(report["billing_changed"]), ["model-0", "model-1"])

    def test_go_case_insensitive_field_overrides_are_seen(self) -> None:
        remote = feed(10)
        remote["model-0"] = {k: v for k, v in remote["model-0"].items() if k != "input_cost_per_token"}
        remote["model-0"]["INPUT_COST_PER_TOKEN"] = 9e-6
        code, _, _, report = self.run_main(feed(10), remote, billed=["model-0"])
        self.assertEqual(code, 1)
        self.assertEqual(report["billing_changed"], {"model-0": ["input_cost_per_token"]})
        for name, extra in {
            "type_error_then_valid": {"INPUT_COST_PER_TOKEN": "bad"},
            "null_does_not_clear_value_field": {"supports_service_tier": True, "SUPPORTS_SERVICE_TIER": None},
            "non_ascii_fold": {"\u017fupports_service_tier": True},
        }.items():
            with self.subTest(name):
                remote = feed(10)
                remote["model-1"] = {**extra, **remote["model-1"]}
                code, _, err, _ = self.run_main(feed(10), remote, billed=["model-1"])
                self.assertEqual(code, 2)
                self.assertIn("model-1", err)

    def test_out_of_range_threshold_is_skipped_like_go(self) -> None:
        remote = feed(10)
        remote["model-0"] = {**remote["model-0"], "long_context_input_token_threshold": 2**63}
        _, _, _, report = self.run_main(feed(10), remote)
        self.assertEqual(report["removed"], ["model-0"])

    def test_unread_cost_field_is_reported_separately(self) -> None:
        remote = feed()
        remote["model-0"] = {**remote["model-0"], "input_cost_per_audio_token": 1e-5}
        code, _, _, report = self.run_main(feed(), remote, billed=["model-0"])
        self.assertEqual(code, 0)
        self.assertEqual(report["unread_cost_changed"], ["model-0"])

    def test_entries_go_would_skip_are_not_priced(self) -> None:
        remote = feed()
        remote["model-0"] = {**remote["model-0"], "input_cost_per_token": "0.01"}
        remote["model-1"] = {"input_cost_per_token": None, "output_cost_per_token": None}
        remote["model-2"] = {**remote["model-2"], "supports_prompt_caching": "yes"}
        remote["model-3"] = [1, 2]
        remote["model-4"] = {"output_cost_per_token": 1e-6, "long_context_input_token_threshold": 1.5}
        code, _, _, report = self.run_main(feed(), remote, billed=["model-0"])
        self.assertEqual(report["remote_priced_models"], 0)
        self.assertEqual(code, 2)  # remote lost every priced entry

    def test_invalid_documents_fail_closed(self) -> None:
        cases = {
            "html": "<html>rate limited</html>",
            "array": "[]",
            "truncated": json.dumps(feed())[:-5],
            "duplicate": '{"a": {"input_cost_per_token": 1}, "a": {"input_cost_per_token": 2}}',
            "nan": '{"a": {"input_cost_per_token": NaN}}',
            "overflow": '{"a": {"input_cost_per_token": 1e400}}',
            "overflow_metadata": '{"a": {"input_cost_per_token": 1, "max_tokens": 1e400}}',

        }
        for name, text in cases.items():
            with self.subTest(name):
                code, _, err, _ = self.run_main(None, None, raw=(json.dumps(feed()), text))
                self.assertEqual(code, 2)
                self.assertIn("INVALID", err)

    def test_sanity_floor_and_shrink_ratio(self) -> None:
        code, _, err, _ = self.run_main(feed(10), feed(10), "--min-priced-models", "11")
        self.assertEqual(code, 2)
        self.assertIn("priced models", err)
        code, _, err, _ = self.run_main(feed(10), feed(7))
        self.assertEqual(code, 2)
        self.assertIn("lost more than 20%", err)
        code, _, _, _ = self.run_main(feed(10), feed(9))
        self.assertEqual(code, 0)

    def test_empty_billed_key_file_is_invalid(self) -> None:
        code, _, err, _ = self.run_main(feed(), feed(), billed=[""])
        self.assertEqual(code, 2)
        self.assertIn("empty", err)

    def test_staleness_warns_only_when_drifting(self) -> None:
        remote = feed()
        remote["model-1"] = {**remote["model-1"], "max_input_tokens": 1}
        nine_days_ago = str(int(time.time() - 9 * 86400 - 60))
        code, out, _, _ = self.run_main(feed(), remote, "--fallback-synced-at", nine_days_ago, "--github")
        self.assertEqual(code, 0)
        self.assertIn("last synced 9.0 days ago", out)
        self.assertIn("::warning title=Pricing fallback::", out)
        recent = str(int(time.time() - 7 * 86400 + 3600))
        code, out, _, _ = self.run_main(feed(), remote, "--fallback-synced-at", recent)
        self.assertIn("WARNING: fallback differs from remote", out)
        self.assertNotIn("last synced", out)
        code, out, _, _ = self.run_main(feed(), feed(), "--fallback-synced-at", "0")
        self.assertEqual(code, 0)
        self.assertNotIn("WARNING", out)

    def test_hostile_key_names_cannot_forge_annotations(self) -> None:
        remote = feed()
        remote["evil\n::error::forged"] = dict(remote["model-0"])
        _, out, _, _ = self.run_main(feed(), remote, "--github")
        self.assertNotIn("\n::error::", out)

    def test_sync_script_rejects_unknown_and_misplaced_flags(self) -> None:
        env = {**os.environ, "PATH": os.environ.get("PATH", "")}
        for arguments in (["check", "--bogus"], ["sync", "--semantic"],
                          ["check", "--billed-keys", "x"], ["check", "--semantic", "--billed-keys", ""],
                          ["check", "--semantic", "--billed-keys"], ["sync", "--github"],
                          ["check", "--github"]):
            with self.subTest(arguments):
                result = subprocess.run(["bash", str(SYNC), *arguments], capture_output=True,
                                        text=True, env=env, check=False)
                self.assertEqual(result.returncode, 2, result.stderr)

    def test_bundled_fallback_is_a_valid_feed(self) -> None:
        bundled = ROOT / "backend/resources/model-pricing/model_prices_and_context_window.json"
        document = diff.load_feed(bundled.read_text(encoding="utf-8"), "bundled")
        self.assertGreaterEqual(len(diff.priced_views(document, "bundled")), 1000)


if __name__ == "__main__":
    unittest.main(verbosity=1)
