#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-check}"
shift || true
SEMANTIC=0
BILLED_KEYS=""
HAVE_BILLED_KEYS=0
GITHUB_ANNOTATIONS=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --semantic) SEMANTIC=1 ;;
    --billed-keys)
      [[ $# -ge 2 && -n "$2" ]] || { echo "--billed-keys needs a non-empty file path" >&2; exit 2; }
      HAVE_BILLED_KEYS=1
      BILLED_KEYS="$2"
      shift
      ;;
    --github) GITHUB_ANNOTATIONS=1 ;;
    *)
      echo "usage: $0 [sync|check [--semantic [--billed-keys FILE] [--github]]|url]" >&2
      exit 2
      ;;
  esac
  shift
done
if [[ "$MODE" != check && ( "$SEMANTIC" -eq 1 || "$HAVE_BILLED_KEYS" -eq 1 || "$GITHUB_ANNOTATIONS" -eq 1 ) ]]; then
  echo "--semantic, --billed-keys and --github apply to check only" >&2
  exit 2
fi
if [[ ( "$HAVE_BILLED_KEYS" -eq 1 || "$GITHUB_ANNOTATIONS" -eq 1 ) && "$SEMANTIC" -eq 0 ]]; then
  echo "--billed-keys and --github require --semantic" >&2
  exit 2
fi
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG_FILE="$REPO_ROOT/backend/internal/config/config.go"
FALLBACK_FILE="$REPO_ROOT/backend/resources/model-pricing/model_prices_and_context_window.json"

REMOTE_URL="$(
  sed -n 's/.*viper.SetDefault("pricing.remote_url", "\(https:[^"]*\)").*/\1/p' "$CONFIG_FILE" | head -n 1
)"

if [[ -z "${REMOTE_URL}" ]]; then
  echo "failed to detect pricing.remote_url from $CONFIG_FILE" >&2
  exit 1
fi
# The URL is printed and kept in CI logs and release evidence: refuse
# embedded credentials, query strings and fragments instead of echoing them.
if [[ ! "$REMOTE_URL" =~ ^https://[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._~/%+-]*$ ]]; then
  echo "pricing.remote_url must be a plain https URL without userinfo, query or fragment" >&2
  exit 1
fi

TMP_FILE="$(mktemp)"
trap 'rm -f "$TMP_FILE"' EXIT

case "$MODE" in
  url)
    echo "$REMOTE_URL"
    exit 0
    ;;
  sync|check)
    ;;
  *)
    echo "usage: $0 [sync|check [--semantic [--billed-keys FILE] [--github]]|url]" >&2
    exit 1
    ;;
esac

curl -fsSL --connect-timeout 20 --max-time 120 --retry 2 "$REMOTE_URL" -o "$TMP_FILE"

if [[ "$MODE" == "sync" ]]; then
  cp "$TMP_FILE" "$FALLBACK_FILE"
  echo "synced $FALLBACK_FILE from $REMOTE_URL"
  exit 0
fi

if [[ "$SEMANTIC" -eq 1 ]]; then
  # Drift alone does not fail: running Gateways bill from the remote feed and
  # the bundled file is only a cold-start fallback. Fail on an unusable feed,
  # or on billed keys affected by the drift when --billed-keys is given.
  args=(--fallback "$FALLBACK_FILE" --remote "$TMP_FILE")
  [[ "$HAVE_BILLED_KEYS" -eq 1 ]] && args+=(--billed-keys "$BILLED_KEYS")
  [[ "$GITHUB_ANNOTATIONS" -eq 1 ]] && args+=(--github)
  synced_at="$(git -C "$REPO_ROOT" log -1 --format=%ct -- "$FALLBACK_FILE" 2>/dev/null || true)"
  if [[ "$synced_at" =~ ^[0-9]+$ ]]; then
    args+=(--fallback-synced-at "$synced_at")
  fi
  echo "remote_url: $REMOTE_URL"
  status=0
  python3 "$REPO_ROOT/tools/pricing_fallback_diff.py" "${args[@]}" || status=$?
  exit "$status"
fi

if ! cmp -s "$TMP_FILE" "$FALLBACK_FILE"; then
  echo "fallback pricing file is out of sync with pricing.remote_url" >&2
  echo "remote_url: $REMOTE_URL" >&2
  echo "run: tools/pricing_fallback_sync.sh sync" >&2
  exit 1
fi

echo "fallback pricing file matches pricing.remote_url"
