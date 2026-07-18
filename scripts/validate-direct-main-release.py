#!/usr/bin/env python3
"""Validate the single-maintainer direct-main image-candidate contract."""

from __future__ import annotations

import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
DOCKER_WORKFLOW = ROOT / ".github" / "workflows" / "docker-publish.yml"
PRICING_WORKFLOW = ROOT / ".github" / "workflows" / "pricing-fallback-sync.yml"


def require(text: str, fragment: str, label: str, errors: list[str]) -> None:
    if fragment not in text:
        errors.append(f"missing {label}")


def forbid(text: str, fragment: str, label: str, errors: list[str]) -> None:
    if fragment in text:
        errors.append(f"forbidden {label}")


def validate() -> list[str]:
    errors: list[str] = []
    docker = DOCKER_WORKFLOW.read_text(encoding="utf-8")
    pricing = PRICING_WORKFLOW.read_text(encoding="utf-8")

    require(docker, "push:\n    branches:\n      - main", "main push trigger", errors)
    require(docker, "if: ${{ github.ref_protected }}", "protected-ref guard", errors)
    require(docker, 'echo "${IMAGE_NAME}:sha-${HEAD_SHA}"', "SHA image tag", errors)
    require(docker, "gateway-coordinate-${{ github.sha }}", "coordinate artifact", errors)
    require(docker, '"workflow_run_id": int(os.environ["GITHUB_RUN_ID"])', "workflow binding", errors)
    forbid(docker, "ENABLE_PUBLIC_IMAGE_PUBLISH", "manual publish variable", errors)
    forbid(docker, 'echo "${IMAGE_NAME}:main"', "mutable main image tag", errors)
    forbid(docker, "make test-unit", "duplicated unit tests", errors)
    forbid(docker, "make test-integration", "duplicated integration tests", errors)

    require(pricing, "push:\n    branches:\n      - main", "pricing main check", errors)
    return errors


def main() -> int:
    errors = validate()
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("PASS: direct-main release workflow contract")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
