#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
hooks_dir="$repo_root/scripts/git-hooks"
if [ ! -x "$hooks_dir/pre-push" ]; then
  echo "ERROR: versioned pre-push hook is missing or not executable" >&2
  exit 1
fi

remote_url="$(git remote get-url origin 2>/dev/null || true)"
case "$remote_url" in
  https://github.com/yuns2023/saiai-server.git|git@github.com:yuns2023/saiai-server.git)
    git config core.hooksPath scripts/git-hooks
    echo "installed versioned hooks for $remote_url"
    ;;
  *)
    echo "ERROR: refusing to install hooks for non-canonical Server remote: ${remote_url:-<none>}" >&2
    exit 1
    ;;
esac
