#!/usr/bin/env python3
"""Verify the SAIAI Server side of the public SAIAI V2 release contract."""

from __future__ import annotations

import json
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


def text(path: str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def verify_bootstrap_contract() -> None:
    contract = json.loads(text("contracts/bootstrap-v2.json"))
    require(contract.get("bootstrap_schema_version") == 2, "bootstrap contract is not schema 2")
    fields = contract["response"]["envelope"]["data"]["capabilities"]["fields"]
    require(
        set(fields)
        == {
            "claude",
            "codex",
            "codex_responses",
            "codex_websockets",
            "openai_messages_dispatch",
        },
        "bootstrap capability contract differs from the public client",
    )

    handler = text("backend/internal/handler/client_handler.go")
    for required in (
        "const clientBootstrapSchemaVersion = 2",
        'json:"openai_messages_dispatch"',
        "capabilities.OpenAIMessagesDispatch = apiKey.Group.AllowMessagesDispatch",
        "case service.PlatformAnthropic, service.PlatformAntigravity:",
        "capabilities.Claude = true",
    ):
        require(required in handler, f"Gateway bootstrap implementation is missing {required!r}")
    require(
        "capabilities.Claude = apiKey.Group.AllowMessagesDispatch" not in handler,
        "OpenAI Messages compatibility still satisfies native Claude setup",
    )


def verify_wrappers() -> None:
    script_dir = ROOT / "scripts" / "saiai-cli"
    public_dir = ROOT / "frontend" / "public" / "saiai-cli"
    for name in ("setup.sh", "setup.ps1", "setup.cmd"):
        canonical = (script_dir / name).read_bytes()
        embedded = (public_dir / name).read_bytes()
        require(canonical == embedded, f"embedded {name} differs from the canonical wrapper")
        source = canonical.decode("utf-8")
        require("https://api.saiai.top/saiai-cli" in source, f"{name} lost the origin placeholder")
        for forbidden in (
            "init-codex",
            "saiai start",
            "ANTHROPIC_AUTH_TOKEN",
            "<api_key>",
            "--api-key",
        ):
            require(forbidden not in source, f"{name} still exposes legacy behavior: {forbidden}")

    shell = text("scripts/saiai-cli/setup.sh")
    powershell = text("scripts/saiai-cli/setup.ps1")
    command = text("scripts/saiai-cli/setup.cmd")
    require("Usage: setup.sh [install]" in shell, "Unix wrapper is not install-only")
    require("${install_path} setup claude" in shell, "Unix wrapper omits absolute first-run guidance")
    require("Invoke-Saiai [install]" in powershell, "PowerShell wrapper is not install-only")
    require("$installPath`\" setup claude" in powershell, "PowerShell wrapper omits absolute guidance")
    require("Usage: setup.cmd [install]" in command, "CMD wrapper is not install-only")
    require('"%INSTALL_PATH%" setup claude' in command, "CMD wrapper omits absolute guidance")


def verify_user_interface() -> None:
    modal = text("frontend/src/components/keys/UseKeyModal.vue")
    for required in (
        "return generateV2PreviewFiles(baseUrl, 'codex')",
        "return generateV2PreviewFiles(baseUrl, 'claude')",
        "$HOME/.local/bin/saiai setup ${product}",
        '$env:LOCALAPPDATA\\\\SAIAI\\\\bin\\\\saiai.exe',
        "%LOCALAPPDATA%\\\\SAIAI\\\\bin\\\\saiai.exe",
        "keys.useKeyModal.sora.description",
    ):
        require(required in modal, f"V2 UI contract is missing {required!r}")
    require(
        modal.count("allowMessagesDispatch") == 1,
        "Messages dispatch changes V2 UI routing instead of remaining ignored metadata",
    )
    for forbidden in (
        "generateClaudeCodeFiles",
        "generateCodexCliFiles",
        "init-codex",
        "codex-ws",
        "claude-compat",
        "saiai start",
    ):
        require(forbidden not in modal, f"V2 UI still exposes removed behavior: {forbidden}")


def verify_activation_and_serving() -> None:
    sync = text("scripts/sync-saiai-cli.sh")
    for required in (
        'readonly DEFAULT_REPO="yuns2023/saiai-client"',
        'readonly DEFAULT_CLIENT_LINK="/var/lib/saiai-server/client-runtime/saiai-cli"',
        "readonly MAX_RELEASE_METADATA_BYTES=4194304",
        "readonly MAX_ASSET_BYTES=67108864",
        "SAIAI_REQUIRE_IMMUTABLE_RELEASE",
        "REQUESTED_MANIFEST_SHA256",
        'case "$ACTION" in',
        "stage_release",
        "activate_release",
        'manifest.get("manifest_schema") != 1',
        'manifest.get("bootstrap_schema_version") != 2',
        'validate_section("assets", binaries)',
        'validate_section("wrappers", wrappers)',
        "validate_trusted_bundle_tree",
        "details.st_uid != expected_uid",
        "stat.S_IMODE(details.st_mode) & 0o022",
        "download_with_stream_limit",
        "download exceeded the {byte_limit}-byte streaming limit",
        "curl -q --proto '=https' --proto-redir '=https'",
        "GH_TOKEN may only be sent to",
        "SAIAI_CLIENT_RELEASES_DIR must use the canonical path",
        "SAIAI_CLIENT_PREVIOUS_LINK must use the canonical path",
        "validate_live_bundle_link",
        "flock -n 9",
        "atomic_symlink",
        "independent V2 runtime root",
        "active and previous links were not changed",
        "first V2 activation",
    ):
        require(required in sync, f"asset activation contract is missing {required!r}")
    require('TAG="${1:-latest}"' not in sync, "asset activation still accepts latest")

    embed = text("backend/internal/web/embed_on.go")
    require('c.Header("Cache-Control", "no-store")' in embed, "mutable assets are cacheable")
    for required in (
        "publicRequestOrigin(req, s.trustedProxyPrefixes)",
        "requestFromTrustedProxy",
        "strictForwardedProto",
        "isPrivateOrLocalIPLiteralAuthority",
        "validPublicDNSName",
        "invalid trusted proxy",
    ):
        require(required in embed, f"executable wrapper origin boundary is missing {required!r}")
    require(
        'req.Header.Get("X-Forwarded-Host")' not in embed,
        "executable wrapper download authority still trusts X-Forwarded-Host",
    )
    require(
        'req.Header.Get("Forwarded")' not in embed,
        "executable wrapper origin still accepts ambiguous Forwarded tuples",
    )
    router = text("backend/internal/server/router.go")
    require(
        "cfg.Server.TrustedProxies..." in router,
        "frontend wrapper rendering does not receive the configured trusted proxy boundary",
    )
    for name in (
        "saiai-linux-x86_64",
        "saiai-linux-aarch64",
        "saiai-macos-x86_64",
        "saiai-macos-aarch64",
        "saiai-windows-x86_64.exe",
        "saiai-windows-aarch64.exe",
        "manifest.json",
    ):
        require(f'"{name}"' in embed, f"Gateway external asset whitelist is missing {name}")


def verify_single_release_authority() -> None:
    workflows = ROOT / ".github" / "workflows"
    require(not (workflows / "saiai-cli-release.yml").exists(), "private CLI publisher still exists")
    require(
        not (workflows / "saiai-desktop-preview.yml").exists(),
        "private Desktop publisher still exists",
    )
    contract_doc = text("docs/V2_GATEWAY_CONTRACT.md")
    require(
        "yuns2023/saiai-client" in contract_doc,
        "contract document does not name the public release authority",
    )
    require(
        "saiai-v0.9.0" in contract_doc,
        "contract document does not pin the production client tag",
    )
    require(
        "092107c40b60cf0174e7278891fbb3cb097ccbe7cc05e8bef05e411687dfa02a"
        in contract_doc,
        "contract document does not pin the independently recorded manifest hash",
    )
    require(
        "stage saiai-v0.9.0 \"$manifest_sha256\"" in contract_doc
        and "activate saiai-v0.9.0 \"$manifest_sha256\"" in contract_doc,
        "contract document does not pass the exact manifest hash to both release operations",
    )
    require(
        "--api-key-stdin" in contract_doc,
        "automation docs omit the stdin credential opt-in",
    )


def verify_ci_gate() -> None:
    workflow = text(".github/workflows/saiai-v2-gateway.yml")
    for required in (
        "persist-credentials: false",
        "python3 scripts/saiai-cli/verify-v2-gateway.py",
        "bash scripts/saiai-cli/test-setup-wrappers.sh",
        "bash scripts/saiai-cli/test-sync-saiai-cli.sh",
        "TestFrontendServer_Middleware|TestPublicRequestOrigin|TestNormalizePublicHost|TestNewFrontendServer",
        "src/components/keys/__tests__/UseKeyModal.spec.ts",
        "--maxWorkers=1",
    ):
        require(required in workflow, f"V2 Gateway CI gate is missing {required!r}")


def main() -> int:
    verify_bootstrap_contract()
    verify_wrappers()
    verify_user_interface()
    verify_activation_and_serving()
    verify_single_release_authority()
    verify_ci_gate()
    print("SAIAI V2 Gateway/public-client integration contract verified")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
