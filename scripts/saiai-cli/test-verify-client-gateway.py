#!/usr/bin/env python3

import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch


MODULE_PATH = Path(__file__).with_name("verify-client-gateway.py")
SPECIFICATION = importlib.util.spec_from_file_location("gateway_contract", MODULE_PATH)
VERIFIER = importlib.util.module_from_spec(SPECIFICATION)
SPECIFICATION.loader.exec_module(VERIFIER)
MODAL_PATH = "frontend/src/components/keys/UseKeyModal.vue"


class UserInterfaceContractTests(unittest.TestCase):
    def setUp(self):
        self.modal = VERIFIER.text(MODAL_PATH)

    def verify_modal(self, modal):
        read_original = VERIFIER.text
        with patch.object(VERIFIER, "text", side_effect=lambda path: modal if path == MODAL_PATH else read_original(path)):
            VERIFIER.verify_user_interface()

    def test_accepts_separate_post_setup_recovery(self):
        self.verify_modal(self.modal)

    def test_rejects_withdrawn_v2_paths(self):
        for withdrawn in ("generateV2PreviewFiles", "getV2GatewayRoot", "setup ${product}", "keys.useKeyModal.v2", "revoke --all"):
            with self.subTest(withdrawn=withdrawn):
                with self.assertRaisesRegex(AssertionError, "withdrawn V2"):
                    self.verify_modal(self.modal + "\n" + withdrawn)

    def test_rejects_recovery_inside_each_setup_generator(self):
        for generator in ("generateClaudeCodeFiles", "generateCodexCliFiles"):
            marker = f"function {generator}(baseUrl: string, apiKey: string): FileConfig[] {{"
            with self.subTest(generator=generator):
                self.assertIn(marker, self.modal)
                mutated = self.modal.replace(marker, marker + "\n  const recovery = 'saiai claude'", 1)
                with self.assertRaisesRegex(AssertionError, "must not replace one-command setup"):
                    self.verify_modal(mutated)

    def test_rejects_missing_recovery_copy_action(self):
        mutated = self.modal.replace("copyCommand('saiai claude', 'saiai claude')", "copyCommand('claude', 'claude')")
        with self.assertRaisesRegex(AssertionError, "post-setup launch guidance is missing"):
            self.verify_modal(mutated)

    def test_rejects_missing_post_setup_section(self):
        mutated = self.modal.replace('data-testid="client-launch"', 'data-testid="other-section"')
        with self.assertRaisesRegex(AssertionError, "post-setup launch guidance is missing"):
            self.verify_modal(mutated)


if __name__ == "__main__":
    unittest.main()
