#!/usr/bin/env bash
set -euo pipefail

remote_name="${1:-origin}"
need_backend="${2:-0}"
need_frontend="${3:-0}"

if [ "$need_backend" -eq 0 ] && [ "$need_frontend" -eq 0 ]; then
  exit 0
fi

remote_url="$(git remote get-url "$remote_name" 2>/dev/null || true)"
case "$remote_url" in
  https://github.com/yuns2023/saiai-server.git|git@github.com:yuns2023/saiai-server.git)
    exit 0
    ;;
  *)
    echo "[repo-role] ERROR: backend/frontend changes require canonical saiai-server.git" >&2
    echo "[repo-role] Current remote: ${remote_url:-<none>}" >&2
    exit 1
    ;;
esac
