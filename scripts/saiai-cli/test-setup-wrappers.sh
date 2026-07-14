#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/../.." && pwd)"
public_dir="${repo_root}/frontend/public/saiai-cli"

for wrapper in setup.sh setup.ps1 setup.cmd; do
  if ! cmp -s "${script_dir}/${wrapper}" "${public_dir}/${wrapper}"; then
    echo "wrapper mirror is out of sync: ${wrapper}" >&2
    exit 1
  fi
done

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT

fixtures="${temporary}/fixtures"
fake_bin="${temporary}/fake-bin"
home="${temporary}/home"
install_dir="${temporary}/install"
output="${temporary}/output"
invoked="${temporary}/binary-invoked"
mkdir -p "${fixtures}" "${fake_bin}" "${home}" "${install_dir}"

asset="${fixtures}/saiai-linux-x86_64"
cat >"${asset}" <<'SH'
#!/usr/bin/env bash
: >"${SAIAI_TEST_BINARY_INVOKED:?}"
SH
chmod +x "${asset}"
sha256="$(sha256sum "${asset}" | awk '{print $1}')"
size="$(wc -c <"${asset}" | tr -d '[:space:]')"
printf '{"manifest_schema":1,"bootstrap_schema_version":2,"version":"0.9.0","assets":{"saiai-linux-x86_64":{"sha256":"%s","size":%s}}}\n' \
  "${sha256}" "${size}" >"${fixtures}/manifest.json"

cat >"${fake_bin}/uname" <<'SH'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
  *) exit 1 ;;
esac
SH
chmod +x "${fake_bin}/uname"

cat >"${fake_bin}/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
destination=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) destination="$2"; shift 2 ;;
    --proto) shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
case "${url}" in
  */manifest.json) cp "${SAIAI_TEST_FIXTURES:?}/manifest.json" "${destination}" ;;
  */saiai-linux-x86_64) cp "${SAIAI_TEST_FIXTURES:?}/saiai-linux-x86_64" "${destination}" ;;
  *) echo "unexpected wrapper URL: ${url}" >&2; exit 1 ;;
esac
SH
chmod +x "${fake_bin}/curl"

run_install() {
  rm -f "${output}" "${invoked}"
  HOME="${home}" \
    PATH="${fake_bin}:${PATH}" \
    SAIAI_DOWNLOAD_BASE="https://download.example.test/client" \
    SAIAI_INSTALL_DIR="${install_dir}" \
    SAIAI_TEST_FIXTURES="${fixtures}" \
    SAIAI_TEST_BINARY_INVOKED="${invoked}" \
    bash "${script_dir}/setup.sh" "$@" >"${output}" 2>&1
}

previous="${temporary}/previous-saiai"
printf 'previous preview client\n' >"${previous}"
cp "${previous}" "${install_dir}/saiai"
chmod +x "${install_dir}/saiai"

run_install install
test -x "${install_dir}/saiai"
cmp -s "${asset}" "${install_dir}/saiai"
cmp -s "${previous}" "${install_dir}/saiai-previous"
test ! -e "${invoked}"
grep -Fq "Next: ${install_dir}/saiai claude or ${install_dir}/saiai codex" "${output}"
grep -Fq "Explicit setup: ${install_dir}/saiai setup claude or ${install_dir}/saiai setup codex" "${output}"

run_install
test -x "${install_dir}/saiai"
test ! -e "${invoked}"

if run_install "https://gateway.example.test" "not-a-key"; then
  echo "install-only wrapper accepted legacy initialization arguments" >&2
  exit 1
fi
grep -Fq "Usage: setup.sh [install]" "${output}"
test ! -e "${invoked}"
if grep -Fq "Checking " "${output}"; then
  echo "install-only wrapper performed network work before rejecting legacy arguments" >&2
  exit 1
fi

if run_install init-codex; then
  echo "install-only wrapper accepted the legacy init-codex command" >&2
  exit 1
fi
grep -Fq "Usage: setup.sh [install]" "${output}"
test ! -e "${invoked}"
if grep -Fq "Checking " "${output}"; then
  echo "install-only wrapper performed network work before rejecting init-codex" >&2
  exit 1
fi

if run_install install unexpected-argument; then
  echo "install-only wrapper accepted an extra argument" >&2
  exit 1
fi
grep -Fq "Usage: setup.sh [install]" "${output}"
test ! -e "${invoked}"
if grep -Fq "Checking " "${output}"; then
  echo "install-only wrapper performed network work before rejecting an extra argument" >&2
  exit 1
fi

for wrapper in setup.sh setup.ps1 setup.cmd; do
  test -s "${script_dir}/${wrapper}"
  grep -Fq 'https://api.saiai.top/saiai-cli' "${script_dir}/${wrapper}"
  for forbidden in \
    'init-codex' \
    'legacy-doctor' \
    'saiai start' \
    'start the local proxy' \
    'ANTHROPIC_AUTH_TOKEN' \
    'CLAUDE_CODE_OAUTH_TOKEN' \
    'SAIAI_CODEX_API_KEY' \
    '<api_key>' \
    '<api-key>' \
    'api_key' \
    ' init "$@"' \
    ' init @Arguments' \
    ' init %*' \
    '.saiai\bin'; do
    if grep -Fqi -- "${forbidden}" "${script_dir}/${wrapper}"; then
      echo "${wrapper} contains a forbidden legacy init/key/start path: ${forbidden}" >&2
      exit 1
    fi
  done
done

grep -Fq 'Invoke-Saiai [install]' "${script_dir}/setup.ps1"
grep -Fq '$provided.Count -gt 1' "${script_dir}/setup.ps1"
grep -Fq '$provided[0] -ne "install"' "${script_dir}/setup.ps1"
grep -Fq '[System.IO.Path]::GetFullPath' "${script_dir}/setup.ps1"
grep -Fq 'Next: & `"$installPath`" claude or & `"$installPath`" codex' "${script_dir}/setup.ps1"
grep -Fq 'saiai-windows-x86_64.exe' "${script_dir}/setup.ps1"
grep -Fq 'saiai-windows-aarch64.exe' "${script_dir}/setup.ps1"
grep -Fq 'saiai-previous.exe' "${script_dir}/setup.ps1"
grep -Fq 'Usage: setup.cmd [install]' "${script_dir}/setup.cmd"
grep -Fq 'if not "%~2"=="" goto usage' "${script_dir}/setup.cmd"
grep -Fq 'if not "%~1"=="" if /I not "%~1"=="install" goto usage' "${script_dir}/setup.cmd"
grep -Fq 'for %%I in ("%INSTALL_PATH%") do set "INSTALL_PATH=%%~fI"' "${script_dir}/setup.cmd"
grep -Fq 'echo Next: "%INSTALL_PATH%" claude or "%INSTALL_PATH%" codex' "${script_dir}/setup.cmd"
grep -Fq 'saiai-windows-x86_64.exe' "${script_dir}/setup.cmd"
grep -Fq 'saiai-windows-aarch64.exe' "${script_dir}/setup.cmd"
grep -Fq 'saiai-previous.exe' "${script_dir}/setup.cmd"

echo "SAIAI V2 install-only wrapper checks passed"
