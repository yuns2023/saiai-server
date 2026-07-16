#!/usr/bin/env python3
"""Verify Gateway integration for the SAIAI global-config client bundle."""

from __future__ import annotations

from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
ASSETS = (
    "saiai-linux-x86_64",
    "saiai-linux-aarch64",
    "saiai-macos-x86_64",
    "saiai-macos-aarch64",
    "saiai-windows-x86_64.exe",
    "saiai-windows-aarch64.exe",
)
WRAPPERS = ("setup.sh", "setup.ps1", "setup.cmd")


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def text(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def verify_user_interface() -> None:
    modal = text("frontend/src/components/keys/UseKeyModal.vue")
    for required in (
        "generateClaudeCodeFiles",
        "generateCodexCliFiles",
        "shellSingleQuote",
        "powershellSingleQuote",
        "shellCliBootstrap",
        "Invoke-Saiai",
        "init-codex",
        "props.apiKey",
        "getGatewayRoot",
    ):
        require(required in modal, f"one-command UI is missing {required!r}")
    for withdrawn in (
        "generateV2PreviewFiles",
        "getV2GatewayRoot",
        "setup ${product}",
        "keys.useKeyModal.v2",
        "saiai claude",
        "revoke --all",
    ):
        require(withdrawn not in modal, f"UI still exposes withdrawn V2 behavior: {withdrawn}")

    tests = text("frontend/src/components/keys/__tests__/UseKeyModal.spec.ts")
    for required in (
        "TEST_ONLY_API_KEY",
        "TEST_ONLY_'_KEY",
        "shell-quotes apostrophes",
        "doubles apostrophes",
        "OpenAI on Codex by default",
        "does not expose withdrawn V2",
    ):
        require(required in tests, f"UI escaping/visibility test is missing {required!r}")
    require("sk-test" not in tests, "UI test uses a key-shaped fixture instead of TEST_ONLY data")

    locales = text("frontend/src/i18n/locales/zh.ts") + text("frontend/src/i18n/locales/en.ts")
    require("SAIAI V2 Preview" not in locales, "locales still advertise V2 Preview")
    require("saiai start" not in locales, "locales still require the removed local proxy")


def verify_activation_and_serving() -> None:
    sync = text("scripts/sync-saiai-cli.sh")
    for required in (
        'readonly DEFAULT_REPO="yuns2023/saiai-client"',
        'readonly DEFAULT_CLIENT_LINK="/var/lib/saiai-server/client-runtime/saiai-cli"',
        'manifest.get("client_mode") == "global-config"',
        'manifest.get("configuration_schema_version") == 1',
        '"bootstrap_schema_version" not in manifest',
        'manifest.get("bootstrap_schema_version") == 2',
        'expected_contract == "retained-live"',
        "is_retained_v2",
        'release.get("prerelease") is not False',
        'validate_section("assets", binaries)',
        'validate_section("wrappers", wrappers)',
        "validate_trusted_bundle_tree",
        "flock -n 9",
        "atomic_symlink",
        "active and previous links were not changed",
    ):
        require(required in sync, f"activation contract is missing {required!r}")
    require('TAG="${1:-latest}"' not in sync, "activation still accepts latest")

    activation_tests = text("scripts/saiai-cli/test-sync-saiai-cli.sh")
    for required in (
        "initial global-config cutover",
        "transition-first-activate.log",
        "global-config activate accepted a retained V2 target",
        "stage accepted a prerelease global-config bundle",
    ):
        require(required in activation_tests, f"transition test is missing {required!r}")

    embed = text("backend/internal/web/embed_on.go")
    for name in (*ASSETS, "manifest.json", *WRAPPERS):
        require(f'"{name}"' in embed, f"external bundle whitelist omits {name}")
    for required in (
        "os.ReadFile(assetPath)",
        "os.Lstat(assetPath)",
        "renderCLIWrapperDownloadBase",
        "publicRequestOrigin(req, s.trustedProxyPrefixes)",
        "requestFromTrustedProxy",
        'c.Header("Cache-Control", "no-store")',
        "configured bundle is incomplete",
    ):
        require(required in embed, f"external wrapper boundary is missing {required!r}")
    require("tryServeCLIWrapper" not in embed, "Gateway still has an embedded-wrapper fallback")
    require('req.Header.Get("X-Forwarded-Host")' not in embed, "wrapper authority trusts X-Forwarded-Host")

    for relative in (
        *(f"scripts/saiai-cli/{name}" for name in WRAPPERS),
        *(f"frontend/public/saiai-cli/{name}" for name in WRAPPERS),
    ):
        require(not (ROOT / relative).exists(), f"embedded wrapper mirror still exists: {relative}")


def verify_ci() -> None:
    workflow = text(".github/workflows/saiai-client-gateway.yml")
    for required in (
        "persist-credentials: false",
        "python3 scripts/saiai-cli/verify-client-gateway.py",
        "bash scripts/saiai-cli/test-sync-saiai-cli.sh",
        "TestFrontendServer_Middleware|TestPublicRequestOrigin|TestNormalizePublicHost|TestNewFrontendServer",
        "src/components/keys/__tests__/UseKeyModal.spec.ts",
        "--maxWorkers=1",
    ):
        require(required in workflow, f"client Gateway CI gate is missing {required!r}")
    require(not (ROOT / ".github/workflows/saiai-v2-gateway.yml").exists(), "V2-named Gateway workflow remains")


def main() -> int:
    verify_user_interface()
    verify_activation_and_serving()
    verify_ci()
    print("SAIAI global-config Gateway integration contract verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
