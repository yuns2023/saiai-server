#!/usr/bin/env python3
"""Exercise pre-push path classification without building project code."""

from __future__ import annotations

import os
import subprocess
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
HOOK = ROOT / "scripts" / "git-hooks" / "pre-push"
ZERO_SHA = "0" * 40


def git(repo: Path, *arguments: str) -> str:
    result = subprocess.run(
        ["git", *arguments],
        cwd=repo,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def commit(repo: Path, message: str) -> str:
    git(repo, "add", "-A")
    git(repo, "commit", "-q", "-m", message)
    return git(repo, "rev-parse", "HEAD")


def run_hook(repo: Path, local_sha: str | None, remote_sha: str | None) -> str:
    update = ""
    if local_sha is not None and remote_sha is not None:
        update = f"refs/heads/test {local_sha} refs/heads/test {remote_sha}\n"
    result = subprocess.run(
        [str(HOOK), "origin", "ignored"],
        cwd=repo,
        input=update,
        check=False,
        capture_output=True,
        text=True,
        env={**os.environ, "PREPUSH_DRY_RUN": "1"},
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr or result.stdout)
    return result.stdout


def require(output: str, expected: str) -> None:
    if expected not in output:
        raise AssertionError(f"missing {expected!r} in hook output: {output!r}")


def main() -> int:
    with tempfile.TemporaryDirectory() as temp_dir:
        repo = Path(temp_dir)
        git(repo, "init", "-q")
        git(repo, "config", "user.name", "SAIAI hook test")
        git(repo, "config", "user.email", "hook-test@example.invalid")

        (repo / "backend").mkdir()
        (repo / "frontend").mkdir()
        (repo / "docs").mkdir()
        (repo / "backend" / "marker.txt").write_text("base\n", encoding="utf-8")
        (repo / "frontend" / "marker.txt").write_text("base\n", encoding="utf-8")
        (repo / "docs" / "note.md").write_text("base\n", encoding="utf-8")
        base = commit(repo, "base")

        (repo / "docs" / "note.md").write_text("docs only\n", encoding="utf-8")
        docs = commit(repo, "docs")

        (repo / "backend" / "quoted\nname.go").write_text(
            "package test\n", encoding="utf-8"
        )
        backend = commit(repo, "backend")

        (repo / "frontend" / "marker.txt").write_text(
            "frontend\n", encoding="utf-8"
        )
        frontend = commit(repo, "frontend")

        require(run_hook(repo, docs, base), "no backend/frontend changes")
        require(run_hook(repo, backend, docs), "backend=1 frontend=0 uncertain=0")
        require(run_hook(repo, frontend, backend), "backend=0 frontend=1 uncertain=0")
        require(run_hook(repo, None, None), "backend=1 frontend=1 uncertain=1")

        git(repo, "update-ref", "refs/remotes/origin/main", base)
        git(
            repo,
            "symbolic-ref",
            "refs/remotes/origin/HEAD",
            "refs/remotes/origin/missing",
        )
        require(run_hook(repo, docs, ZERO_SHA), "no backend/frontend changes")

    print("PASS: pre-push hook scopes docs, backend, frontend, and uncertain pushes")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
