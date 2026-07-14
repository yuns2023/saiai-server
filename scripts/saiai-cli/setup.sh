#!/usr/bin/env bash
#
# Install the SAIAI client, optionally without initializing any client config.
# Existing Claude Code and Codex CLI initialization entry points remain available.
#
# V2 Preview (binary only; then run `saiai setup` interactively):
#   curl -fsSL https://api.saiai.top/saiai-cli/setup.sh | bash -s -- install
#
# Claude Code:
#   curl -fsSL https://api.saiai.top/saiai-cli/setup.sh | bash -s -- "https://api.saiai.top" "<api_key>"
#
# Codex CLI:
#   curl -fsSL https://api.saiai.top/saiai-cli/setup.sh | bash -s -- init-codex "https://api.saiai.top" "<api_key>"

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  setup.sh install                                            # V2 Preview: install only
  setup.sh "<base_url>" "<api_key>"                         # Claude Code
  setup.sh init-codex "<base_url>" "<api_key>" [--websockets] # Codex CLI

Environment variables:
  SAIAI_DOWNLOAD_BASE     Optional binary base URL override (default: https://api.saiai.top/saiai-cli)
  SAIAI_INSTALL_DIR           Optional install directory (default: ~/.local/bin)

After Claude Code setup, start the local proxy:
  saiai start
EOF
}

INSTALL_ONLY=0
if [ "${1:-}" = "install" ]; then
  if [ "$#" -ne 1 ]; then
    echo "The install mode accepts no additional arguments. Run 'saiai setup' after installation." >&2
    usage >&2
    exit 1
  fi
  INSTALL_ONLY=1
elif [ "$#" -lt 2 ]; then
  usage >&2
  exit 1
fi

DEFAULT_DOWNLOAD_BASE="https://api.saiai.top/saiai-cli"
INSTALL_DIR="${SAIAI_INSTALL_DIR:-"$HOME/.local/bin"}"

OS="$(uname -s)"
ARCH="$(uname -m)"

case "${OS}" in
  Linux) PLATFORM="linux" ;;
  Darwin) PLATFORM="macos" ;;
  *)
    echo "Unsupported OS: ${OS}. SAIAI one-line install currently provides Linux and macOS assets." >&2
    exit 1
    ;;
esac

case "${ARCH}" in
  x86_64|amd64) ARCH_NAME="x86_64" ;;
  arm64|aarch64) ARCH_NAME="aarch64" ;;
  *)
    echo "Unsupported architecture: ${ARCH}" >&2
    exit 1
    ;;
esac

ASSET_NAME="saiai-${PLATFORM}-${ARCH_NAME}"
if [ -n "${SAIAI_DOWNLOAD_BASE:-}" ]; then
  DOWNLOAD_BASE="${SAIAI_DOWNLOAD_BASE%/}"
else
  DOWNLOAD_BASE="${DEFAULT_DOWNLOAD_BASE}"
fi
DOWNLOAD_URL="${DOWNLOAD_BASE}/${ASSET_NAME}"
MANIFEST_URL="${DOWNLOAD_BASE}/manifest.json"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT
BIN_PATH="${TMP_DIR}/saiai"
MANIFEST_PATH="${TMP_DIR}/manifest.json"
INSTALL_PATH="${INSTALL_DIR}/saiai"

mkdir -p "${INSTALL_DIR}"

python_bin() {
  if command -v python3 >/dev/null 2>&1; then
    command -v python3
  elif command -v python >/dev/null 2>&1; then
    command -v python
  else
    return 1
  fi
}

manifest_asset_sha256() {
  local manifest_path="$1"
  local asset_name="$2"
  local py
  py="$(python_bin)" || {
    echo "Python is required to read ${MANIFEST_URL}" >&2
    return 1
  }
  "$py" - "$manifest_path" "$asset_name" <<'PY'
import json
import sys

manifest_path = sys.argv[1]
asset_name = sys.argv[2]
try:
    with open(manifest_path, "r") as handle:
        manifest = json.load(handle)
    asset = manifest.get("assets", {}).get(asset_name)
    sha256 = asset.get("sha256") if isinstance(asset, dict) else None
    if not sha256:
        sys.stderr.write("manifest does not include sha256 for {0}\n".format(asset_name))
        sys.exit(1)
    print(sha256)
except Exception as exc:
    sys.stderr.write("failed to parse manifest: {0}\n".format(exc))
    sys.exit(1)
PY
}

sha256_hex() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    echo "No SHA-256 tool found. Install sha256sum, shasum, or openssl." >&2
    return 1
  fi
}

echo "Checking ${MANIFEST_URL}" >&2
curl -fsSL -o "${MANIFEST_PATH}" "${MANIFEST_URL}"
EXPECTED_SHA256="$(manifest_asset_sha256 "${MANIFEST_PATH}" "${ASSET_NAME}" | tr 'A-F' 'a-f')"
if [ "${#EXPECTED_SHA256}" -ne 64 ]; then
  echo "Invalid sha256 in ${MANIFEST_URL} for ${ASSET_NAME}: ${EXPECTED_SHA256}" >&2
  exit 1
fi

echo "Downloading ${DOWNLOAD_URL}" >&2
curl -fsSL -o "${BIN_PATH}" "${DOWNLOAD_URL}"
ACTUAL_SHA256="$(sha256_hex "${BIN_PATH}" | tr 'A-F' 'a-f')"
if [ "${ACTUAL_SHA256}" != "${EXPECTED_SHA256}" ]; then
  echo "SHA-256 mismatch for ${ASSET_NAME}" >&2
  echo "  expected: ${EXPECTED_SHA256}" >&2
  echo "  actual:   ${ACTUAL_SHA256}" >&2
  exit 1
fi
chmod +x "${BIN_PATH}"
mv -f "${BIN_PATH}" "${INSTALL_PATH}"
chmod +x "${INSTALL_PATH}"

if [ "${INSTALL_ONLY}" -eq 1 ]; then
  EXIT_CODE=0
elif [ "$1" = "init-codex" ]; then
  shift
  "${INSTALL_PATH}" init-codex "$@"
  EXIT_CODE=$?
else
  "${INSTALL_PATH}" init "$@"
  EXIT_CODE=$?
fi

if [ "${EXIT_CODE}" -eq 0 ]; then
  echo "SAIAI installed at ${INSTALL_PATH}" >&2
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*) ;;
    *)
      echo "Note: ${INSTALL_DIR} is not in PATH. Add it to PATH or run ${INSTALL_PATH} directly." >&2
      ;;
  esac
  if [ "${INSTALL_ONLY}" -eq 1 ]; then
    echo "Next, run: saiai setup" >&2
  else
    echo "For Claude Code, start the local proxy with: saiai start" >&2
  fi
fi
exit "${EXIT_CODE}"
