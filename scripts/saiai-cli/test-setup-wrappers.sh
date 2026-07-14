#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
PUBLIC_DIR="${REPO_ROOT}/frontend/public/saiai-cli"

for wrapper in setup.sh setup.ps1 setup.cmd; do
  if ! cmp -s "${SCRIPT_DIR}/${wrapper}" "${PUBLIC_DIR}/${wrapper}"; then
    echo "wrapper mirror is out of sync: ${wrapper}" >&2
    exit 1
  fi
done

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

FIXTURES="${TMP_DIR}/fixtures"
FAKE_BIN_DIR="${TMP_DIR}/fake-bin"
HOME_DIR="${TMP_DIR}/home"
INSTALL_DIR="${TMP_DIR}/install"
INVOCATION_LOG="${TMP_DIR}/invocation.log"
OUTPUT_LOG="${TMP_DIR}/output.log"
mkdir -p "${FIXTURES}" "${FAKE_BIN_DIR}" "${HOME_DIR}" "${INSTALL_DIR}"

ASSET_PATH="${FIXTURES}/saiai-linux-x86_64"
MANIFEST_PATH="${FIXTURES}/manifest.json"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'printf '\''%s\n'\'' "$@" > "${SAIAI_TEST_INVOCATION_LOG:?}"' \
  > "${ASSET_PATH}"
chmod +x "${ASSET_PATH}"

ASSET_SHA256="$(sha256sum "${ASSET_PATH}" | awk '{print $1}')"
printf '{"assets":{"saiai-linux-x86_64":{"sha256":"%s"}}}\n' \
  "${ASSET_SHA256}" > "${MANIFEST_PATH}"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'case "${1:-}" in' \
  '  -s) printf '\''Linux\n'\'' ;;' \
  '  -m) printf '\''x86_64\n'\'' ;;' \
  '  *) exit 1 ;;' \
  'esac' \
  > "${FAKE_BIN_DIR}/uname"
chmod +x "${FAKE_BIN_DIR}/uname"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'output=' \
  'url=' \
  'while [ "$#" -gt 0 ]; do' \
  '  case "$1" in' \
  '    -o) output="$2"; shift 2 ;;' \
  '    -*) shift ;;' \
  '    *) url="$1"; shift ;;' \
  '  esac' \
  'done' \
  'case "${url}" in' \
  '  */manifest.json) cp "${SAIAI_TEST_MANIFEST:?}" "${output}" ;;' \
  '  */saiai-linux-x86_64) cp "${SAIAI_TEST_ASSET:?}" "${output}" ;;' \
  '  *) printf '\''unexpected URL: %s\n'\'' "${url}" >&2; exit 1 ;;' \
  'esac' \
  > "${FAKE_BIN_DIR}/curl"
chmod +x "${FAKE_BIN_DIR}/curl"

run_wrapper() {
  rm -f "${INVOCATION_LOG}" "${OUTPUT_LOG}"
  HOME="${HOME_DIR}" \
    PATH="${FAKE_BIN_DIR}:${PATH}" \
    SAIAI_DOWNLOAD_BASE="https://download.test/saiai-cli" \
    SAIAI_INSTALL_DIR="${INSTALL_DIR}" \
    SAIAI_TEST_ASSET="${ASSET_PATH}" \
    SAIAI_TEST_MANIFEST="${MANIFEST_PATH}" \
    SAIAI_TEST_INVOCATION_LOG="${INVOCATION_LOG}" \
    bash "${SCRIPT_DIR}/setup.sh" "$@" > "${OUTPUT_LOG}" 2>&1
}

run_wrapper install
test -x "${INSTALL_DIR}/saiai"
test ! -e "${INVOCATION_LOG}"
grep -Fq "Next, run: saiai setup" "${OUTPUT_LOG}"
if grep -Fq "start the local proxy" "${OUTPUT_LOG}"; then
  echo "install-only mode printed legacy initialization guidance" >&2
  exit 1
fi

if run_wrapper install unexpected-argument; then
  echo "install-only mode accepted an extra argument" >&2
  exit 1
fi
grep -Fq "accepts no additional arguments" "${OUTPUT_LOG}"
if grep -Fq "Checking " "${OUTPUT_LOG}"; then
  echo "install-only mode performed work before rejecting an extra argument" >&2
  exit 1
fi
test ! -e "${INVOCATION_LOG}"

run_wrapper "https://gateway.test" "test-key"
printf '%s\n' init "https://gateway.test" "test-key" > "${TMP_DIR}/expected.log"
cmp -s "${TMP_DIR}/expected.log" "${INVOCATION_LOG}"

run_wrapper init-codex "https://gateway.test" "test-key" --websockets
printf '%s\n' init-codex "https://gateway.test" "test-key" --websockets > "${TMP_DIR}/expected.log"
cmp -s "${TMP_DIR}/expected.log" "${INVOCATION_LOG}"

grep -Fq 'Invoke-Saiai install' "${SCRIPT_DIR}/setup.ps1"
grep -Fq 'if ($installOnly)' "${SCRIPT_DIR}/setup.ps1"
grep -Fq 'setup.cmd install' "${SCRIPT_DIR}/setup.cmd"
grep -Fq 'if "%INSTALLONLY%"=="1"' "${SCRIPT_DIR}/setup.cmd"
if grep -Fq '.saiai\bin' "${SCRIPT_DIR}/setup.ps1" || \
   grep -Fq '.saiai\bin' "${SCRIPT_DIR}/setup.cmd"; then
  echo "V2 install-only wrappers contain a legacy Windows install fallback" >&2
  exit 1
fi

echo "setup wrapper tests passed"
