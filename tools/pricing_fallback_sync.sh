#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-check}"
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
    echo "usage: $0 [sync|check|url]" >&2
    exit 1
    ;;
esac

curl -fsSL "$REMOTE_URL" -o "$TMP_FILE"

if [[ "$MODE" == "sync" ]]; then
  cp "$TMP_FILE" "$FALLBACK_FILE"
  echo "synced $FALLBACK_FILE from $REMOTE_URL"
  exit 0
fi

if ! cmp -s "$TMP_FILE" "$FALLBACK_FILE"; then
  echo "fallback pricing file is out of sync with pricing.remote_url" >&2
  echo "remote_url: $REMOTE_URL" >&2
  echo "run: tools/pricing_fallback_sync.sh sync" >&2
  exit 1
fi

echo "fallback pricing file matches pricing.remote_url"
