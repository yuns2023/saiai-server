#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
sync_script="${script_dir}/../sync-saiai-cli.sh"
temporary="$(mktemp -d)"
cleanup() {
  chmod -R u+rwX "$temporary" 2>/dev/null || true
  rm -rf "$temporary"
}
trap cleanup EXIT

fixtures="${temporary}/fixtures"
fake_bin="${temporary}/bin"
curl_log="${temporary}/curl.log"
mkdir -p "${fixtures}/assets" "${fixtures}/releases" "$fake_bin"
: >"$curl_log"

make_release() {
  local tag="$1"
  local version="$2"
  local first_id="$3"
  local marker="$4"
  local immutable="$5"
  python3 - "$fixtures" "$tag" "$version" "$first_id" "$marker" "$immutable" <<'PY'
import hashlib
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
tag = sys.argv[2]
version = sys.argv[3]
first_id = int(sys.argv[4])
marker = sys.argv[5]
immutable = sys.argv[6] == "true"
binaries = (
    "saiai-linux-x86_64",
    "saiai-linux-aarch64",
    "saiai-macos-x86_64",
    "saiai-macos-aarch64",
    "saiai-windows-x86_64.exe",
    "saiai-windows-aarch64.exe",
)
wrappers = ("setup.sh", "setup.ps1", "setup.cmd")
release_dir = root / "releases" / tag
release_dir.mkdir(parents=True, exist_ok=True)

for name in binaries:
    (release_dir / name).write_bytes(f"{name}:{marker}\n".encode())
for name in wrappers:
    (release_dir / name).write_bytes(f"{name}:{marker}:install-only\n".encode())

def metadata(path):
    contents = path.read_bytes()
    return {"sha256": hashlib.sha256(contents).hexdigest(), "size": len(contents)}

manifest = {
    "manifest_schema": 1,
    "bootstrap_schema_version": 2,
    "version": version,
    "assets": {name: metadata(release_dir / name) for name in binaries},
    "wrappers": {name: metadata(release_dir / name) for name in wrappers},
}
(release_dir / "manifest.json").write_text(
    json.dumps(manifest, sort_keys=True, separators=(",", ":")) + "\n",
    encoding="utf-8",
)

release_assets = []
for offset, name in enumerate(("manifest.json",) + binaries + wrappers):
    asset_id = first_id + offset
    destination = root / "assets" / str(asset_id)
    destination.write_bytes((release_dir / name).read_bytes())
    release_assets.append({"id": asset_id, "name": name})

(release_dir / "release.json").write_text(
    json.dumps(
        {
            "tag_name": tag,
            "draft": False,
            "prerelease": True,
            "immutable": immutable,
            "assets": release_assets,
        }
    ),
    encoding="utf-8",
)
PY
}

manifest_hash() {
  python3 - "$1" <<'PY'
import hashlib
import pathlib
import sys

print(hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest())
PY
}

make_release saiai-v0.9.0 0.9.0 100 first false
make_release saiai-v0.9.1 0.9.1 200 second false
make_release saiai-v0.9.2 0.9.2 300 corrupt false
make_release saiai-v0.9.3 0.9.3 400 local-corrupt false
make_release saiai-v0.9.4 0.9.4 500 immutable true
make_release saiai-v0.9.5 0.9.5 700 oversized-metadata false

first_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.0/manifest.json")"
second_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.1/manifest.json")"
corrupt_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.2/manifest.json")"
local_corrupt_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.3/manifest.json")"
immutable_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.4/manifest.json")"
oversized_metadata_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.5/manifest.json")"

# The fake transport does not provide Content-Length and deliberately ignores
# curl's --max-filesize hint. Only the streaming limiter can reject this body.
python3 - "${fixtures}/releases/saiai-v0.9.5/release.json" <<'PY'
import pathlib
import sys

pathlib.Path(sys.argv[1]).write_bytes(b"x" * (4 * 1024 * 1024 + 1))
PY

# Leave the release metadata coherent but corrupt one downloaded binary.
printf 'tampered\n' >>"${fixtures}/assets/301"

cat >"${fake_bin}/curl" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
[ "${1:-}" = "-q" ] || {
  echo "curl -q was not the first curl argument" >&2
  exit 95
}
output=""
url=""
max_filesize=""
protocols=""
redirect_protocols=""
config_disabled=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    -q)
      config_disabled=1
      shift
      ;;
    -o)
      output="$2"
      shift 2
      ;;
    -H|--retry|--connect-timeout)
      shift 2
      ;;
    --max-filesize)
      max_filesize="$2"
      shift 2
      ;;
    --proto)
      protocols="$2"
      shift 2
      ;;
    --proto-redir)
      redirect_protocols="$2"
      shift 2
      ;;
    -*)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done
[ -n "$url" ]
[ "$config_disabled" = "1" ]
[ "$protocols" = "=https" ] || {
  echo "curl request did not restrict its initial protocol to HTTPS" >&2
  exit 94
}
[ "$redirect_protocols" = "=https" ] || {
  echo "curl request did not restrict redirect protocols to HTTPS" >&2
  exit 93
}
printf '%s\n' "$url" >>"${SAIAI_TEST_CURL_LOG:?}"
if [ "${SAIAI_TEST_OFFLINE:-0}" = "1" ]; then
  echo "curl must not run during offline activation" >&2
  exit 97
fi
emit_file() {
  local source="$1"
  if [ -n "$output" ]; then
    cp "$source" "$output"
  else
    cat "$source"
  fi
}
case "$url" in
  https://api.test/repos/yuns2023/saiai-client/releases/tags/saiai-v*|https://api.github.com/repos/yuns2023/saiai-client/releases/tags/saiai-v*)
    [ "$max_filesize" = "4194304" ] || {
      echo "release metadata download omitted the 4 MiB curl limit" >&2
      exit 92
    }
    tag="${url##*/}"
    emit_file "${SAIAI_TEST_FIXTURES:?}/releases/${tag}/release.json"
    ;;
  https://api.test/repos/yuns2023/saiai-client/releases/assets/*|https://api.github.com/repos/yuns2023/saiai-client/releases/assets/*)
    [ "$max_filesize" = "67108864" ] || {
      echo "release asset download omitted the 64 MiB curl limit" >&2
      exit 96
    }
    asset_id="${url##*/}"
    emit_file "${SAIAI_TEST_FIXTURES:?}/assets/${asset_id}"
    ;;
  *)
    echo "unexpected curl URL: $url" >&2
    exit 1
    ;;
esac
SH
chmod +x "${fake_bin}/curl"

active="${temporary}/runtime/saiai-cli"
run_for() {
  local client_dir="$1"
  shift
  env -u GH_TOKEN \
    PATH="${fake_bin}:${PATH}" \
    SAIAI_TEST_FIXTURES="$fixtures" \
    SAIAI_TEST_CURL_LOG="$curl_log" \
    SAIAI_GITHUB_API_URL="https://api.test" \
    SAIAI_CLIENT_DIR="$client_dir" \
    bash "$sync_script" "$@"
}

if run_for "$active" stage latest "$first_hash" >"${temporary}/invalid-tag.log" 2>&1; then
  echo "stage accepted a mutable latest selector" >&2
  exit 1
fi
grep -Fq "explicit release tag" "${temporary}/invalid-tag.log"

if run_for "$active" stage saiai-v0.9.0 bad-hash >"${temporary}/invalid-hash.log" 2>&1; then
  echo "activate accepted an invalid manifest hash" >&2
  exit 1
fi
grep -Fq "64 lowercase hexadecimal" "${temporary}/invalid-hash.log"

if run_for "$active" stage saiai-v0.9.5 "$oversized_metadata_hash" >"${temporary}/oversized-metadata.log" 2>&1; then
  echo "stage accepted release metadata beyond the streaming byte limit" >&2
  exit 1
fi
grep -Fq "download exceeded the 4194304-byte streaming limit" "${temporary}/oversized-metadata.log"
test ! -e "${active}.releases/saiai-v0.9.5"
grep -Fq 'download_with_stream_limit "${STAGE_DIR}/${name}" "$MAX_ASSET_BYTES"' "$sync_script"

if env -u GH_TOKEN \
  PATH="${fake_bin}:${PATH}" \
  SAIAI_TEST_FIXTURES="$fixtures" \
  SAIAI_TEST_CURL_LOG="$curl_log" \
  SAIAI_GITHUB_API_URL="https://api.test" \
  SAIAI_CLIENT_DIR="$active" \
  SAIAI_REQUIRE_IMMUTABLE_RELEASE=1 \
  bash "$sync_script" stage saiai-v0.9.0 "$first_hash" >"${temporary}/require-immutable.log" 2>&1; then
  echo "stage ignored the required GitHub immutable flag" >&2
  exit 1
fi
grep -Fq "is not marked immutable" "${temporary}/require-immutable.log"
test ! -e "$active"
test ! -e "${active}.previous"

wrong_hash="$(printf '0%.0s' {1..64})"
if run_for "$active" stage saiai-v0.9.0 "$wrong_hash" >"${temporary}/wrong-stage-hash.log" 2>&1; then
  echo "stage accepted a manifest hash that was not independently pinned" >&2
  exit 1
fi
grep -Fq "manifest sha256 mismatch" "${temporary}/wrong-stage-hash.log"
test ! -e "${active}.releases/saiai-v0.9.0"

if ! run_for "$active" stage saiai-v0.9.0 "$first_hash" >"${temporary}/first-stage.log" 2>&1; then
  sed -n '1,200p' "${temporary}/first-stage.log" >&2
  exit 1
fi
grep -Fq "active and previous links were not changed" "${temporary}/first-stage.log"
grep -Fq "manifest sha:  ${first_hash}" "${temporary}/first-stage.log"
test ! -e "$active"
test ! -e "${active}.previous"

first_tag_dir="${active}.releases/saiai-v0.9.0"
first_release="${first_tag_dir}/${first_hash}"
test -d "$first_release"
test "$(stat -c '%a' "$first_tag_dir")" = "555"
test "$(stat -c '%a' "$first_release")" = "555"
test "$(stat -c '%a' "${first_release}/manifest.json")" = "444"
test "$(stat -c '%a' "${first_release}/saiai-linux-x86_64")" = "555"
test "$(stat -c '%a' "${first_release}/setup.sh")" = "555"
test "$(stat -c '%a' "${first_release}/setup.ps1")" = "444"
test "$(find "$first_release" -mindepth 1 -maxdepth 1 -type f | wc -l)" -eq 10
test "$(stat -c '%a' "${active}.releases.lock")" = "600"

# Existing tag, bundle, and files must remain owned by this euid and must never
# become group/world writable before they are reused or chmodded.
grep -Fq "details.st_uid != expected_uid" "$sync_script"
chmod g+w "$first_tag_dir"
if run_for "$active" stage saiai-v0.9.0 "$first_hash" >"${temporary}/writable-tag.log" 2>&1; then
  echo "stage accepted a group-writable tag directory" >&2
  exit 1
fi
grep -Fq "SAIAI release tag directory must not be group- or world-writable" "${temporary}/writable-tag.log"
chmod g-w "$first_tag_dir"

chmod g+w "$first_release"
if run_for "$active" stage saiai-v0.9.0 "$first_hash" >"${temporary}/writable-bundle.log" 2>&1; then
  echo "stage accepted a group-writable bundle directory" >&2
  exit 1
fi
grep -Fq "immutable bundle must not be group- or world-writable" "${temporary}/writable-bundle.log"
chmod g-w "$first_release"

chmod g+w "${first_release}/saiai-linux-x86_64"
if run_for "$active" stage saiai-v0.9.0 "$first_hash" >"${temporary}/writable-bundle-file.log" 2>&1; then
  echo "stage accepted a group-writable bundle file" >&2
  exit 1
fi
grep -Fq "immutable bundle entry saiai-linux-x86_64 must not be group- or world-writable" "${temporary}/writable-bundle-file.log"
chmod g-w "${first_release}/saiai-linux-x86_64"

# Re-staging an identical bundle restores all immutable file/directory modes.
chmod 0755 "$first_tag_dir" "$first_release"
chmod 0644 "${first_release}/saiai-linux-x86_64" "${first_release}/setup.sh"
chmod 0755 "${first_release}/manifest.json" "${first_release}/setup.ps1"
run_for "$active" stage saiai-v0.9.0 "$first_hash" >"${temporary}/permission-repair.log" 2>&1
test "$(stat -c '%a' "$first_tag_dir")" = "555"
test "$(stat -c '%a' "$first_release")" = "555"
test "$(stat -c '%a' "${first_release}/saiai-linux-x86_64")" = "555"
test "$(stat -c '%a' "${first_release}/setup.sh")" = "555"
test "$(stat -c '%a' "${first_release}/manifest.json")" = "444"
test "$(stat -c '%a' "${first_release}/setup.ps1")" = "444"
test ! -e "$active"

test_token="github-token-must-not-be-printed"
GH_TOKEN="$test_token" \
  PATH="${fake_bin}:${PATH}" \
  SAIAI_TEST_FIXTURES="$fixtures" \
  SAIAI_TEST_CURL_LOG="$curl_log" \
  SAIAI_GITHUB_API_URL="https://api.github.com" \
  SAIAI_CLIENT_DIR="$active" \
  bash "$sync_script" stage saiai-v0.9.0 "$first_hash" >"${temporary}/token.log" 2>&1
if grep -Fq "$test_token" "${temporary}/token.log"; then
  echo "stage printed the GitHub token" >&2
  exit 1
fi

curl_calls_before="$(wc -l <"$curl_log")"
if GH_TOKEN="$test_token" \
  PATH="${fake_bin}:${PATH}" \
  SAIAI_TEST_FIXTURES="$fixtures" \
  SAIAI_TEST_CURL_LOG="$curl_log" \
  SAIAI_GITHUB_API_URL="https://api.test" \
  SAIAI_CLIENT_DIR="$active" \
  bash "$sync_script" stage saiai-v0.9.0 "$first_hash" >"${temporary}/custom-api-token.log" 2>&1; then
  echo "stage sent a GitHub token to a custom API origin" >&2
  exit 1
fi
grep -Fq "GH_TOKEN may only be sent to https://api.github.com" "${temporary}/custom-api-token.log"
test "$(wc -l <"$curl_log")" = "$curl_calls_before"

# The same tag may never acquire a second manifest hash.
make_release saiai-v0.9.0 0.9.0 600 retagged false
retagged_hash="$(manifest_hash "${fixtures}/releases/saiai-v0.9.0/manifest.json")"
if run_for "$active" stage saiai-v0.9.0 "$retagged_hash" >"${temporary}/retagged.log" 2>&1; then
  echo "stage accepted changed content for an already staged tag" >&2
  exit 1
fi
grep -Fq "refusing a mutable tag" "${temporary}/retagged.log"
test ! -e "$active"
# Restore the original release fixture for independent-runtime-root tests below.
make_release saiai-v0.9.0 0.9.0 100 first false

if run_for "$active" stage saiai-v0.9.2 "$corrupt_hash" >"${temporary}/corrupt-download.log" 2>&1; then
  echo "stage accepted a corrupt downloaded release" >&2
  exit 1
fi
grep -Fq "mismatch for saiai-linux-x86_64" "${temporary}/corrupt-download.log"
test ! -e "$active"

run_for "$active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/second-stage.log" 2>&1
second_release="${active}.releases/saiai-v0.9.1/${second_hash}"
test -d "$second_release"
test ! -e "$active"
test ! -e "${active}.previous"

# All live paths must be siblings in one trusted runtime parent.
different_parent_active="${temporary}/different-parent/runtime/saiai-cli"
if env -u GH_TOKEN \
  PATH="${fake_bin}:${PATH}" \
  SAIAI_TEST_FIXTURES="$fixtures" \
  SAIAI_TEST_CURL_LOG="$curl_log" \
  SAIAI_GITHUB_API_URL="https://api.test" \
  SAIAI_CLIENT_DIR="$different_parent_active" \
  SAIAI_CLIENT_RELEASES_DIR="${temporary}/different-parent/releases/saiai-cli.releases" \
  bash "$sync_script" stage saiai-v0.9.1 "$second_hash" >"${temporary}/different-parent.log" 2>&1; then
  echo "stage accepted live paths from different runtime parents" >&2
  exit 1
fi
grep -Fq "must share one runtime parent" "${temporary}/different-parent.log"

noncanonical_active="${temporary}/noncanonical/runtime/saiai-cli"
if env -u GH_TOKEN \
  PATH="${fake_bin}:${PATH}" \
  SAIAI_TEST_FIXTURES="$fixtures" \
  SAIAI_TEST_CURL_LOG="$curl_log" \
  SAIAI_GITHUB_API_URL="https://api.test" \
  SAIAI_CLIENT_DIR="$noncanonical_active" \
  SAIAI_CLIENT_RELEASES_DIR="${noncanonical_active}.other-releases" \
  bash "$sync_script" stage saiai-v0.9.1 "$second_hash" >"${temporary}/noncanonical-layout.log" 2>&1; then
  echo "stage accepted a second sibling releases identity for one active link" >&2
  exit 1
fi
grep -Fq "must use the canonical path ${noncanonical_active}.releases" "${temporary}/noncanonical-layout.log"

if run_for "${temporary}/invalid-basename/.." stage saiai-v0.9.1 "$second_hash" >"${temporary}/invalid-basename.log" 2>&1; then
  echo "stage accepted '..' as a live-path basename" >&2
  exit 1
fi
grep -Fq "invalid path" "${temporary}/invalid-basename.log"

unsafe_active="${temporary}/unsafe-runtime/saiai-cli"
mkdir -p "$(dirname "$unsafe_active")"
chmod 0775 "$(dirname "$unsafe_active")"
if run_for "$unsafe_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/unsafe-parent.log" 2>&1; then
  echo "stage accepted a group-writable runtime parent" >&2
  exit 1
fi
grep -Fq "must not be group- or world-writable" "${temporary}/unsafe-parent.log"

unsafe_releases_active="${temporary}/unsafe-releases/saiai-cli"
mkdir -p "$(dirname "$unsafe_releases_active")" "${unsafe_releases_active}.releases"
chmod 0755 "$(dirname "$unsafe_releases_active")"
chmod 0775 "${unsafe_releases_active}.releases"
if run_for "$unsafe_releases_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/unsafe-releases.log" 2>&1; then
  echo "stage accepted a group-writable releases root" >&2
  exit 1
fi
grep -Fq "SAIAI client releases root must not be group- or world-writable" "${temporary}/unsafe-releases.log"

unsafe_lock_active="${temporary}/unsafe-lock/saiai-cli"
mkdir -p "$(dirname "$unsafe_lock_active")" "${unsafe_lock_active}.releases"
chmod 0755 "$(dirname "$unsafe_lock_active")" "${unsafe_lock_active}.releases"
: >"${unsafe_lock_active}.releases.lock"
chmod 0660 "${unsafe_lock_active}.releases.lock"
if run_for "$unsafe_lock_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/unsafe-lock.log" 2>&1; then
  echo "stage accepted a group-writable shared lock" >&2
  exit 1
fi
grep -Fq "SAIAI shared release lock must not be group- or world-writable" "${temporary}/unsafe-lock.log"

lock_attack_active="${temporary}/lock-attack/saiai-cli"
mkdir -p "$(dirname "$lock_attack_active")" "${lock_attack_active}.releases"
chmod 0755 "$(dirname "$lock_attack_active")" "${lock_attack_active}.releases"
printf 'keep-lock-target\n' >"${temporary}/lock-target"
ln -s "${temporary}/lock-target" "${lock_attack_active}.releases.lock"
if run_for "$lock_attack_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/lock-symlink.log" 2>&1; then
  echo "stage followed a symlink at the shared lock path" >&2
  exit 1
fi
grep -Fq "shared release lock must not be a symlink" "${temporary}/lock-symlink.log"
grep -Fq "keep-lock-target" "${temporary}/lock-target"

# A wrong hash is rejected without networking or changing live links.
curl_calls_before="$(wc -l <"$curl_log")"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.0 "$(printf '0%.0s' {1..64})" >"${temporary}/wrong-hash.log" 2>&1; then
  echo "activate accepted a wrong manifest hash" >&2
  exit 1
fi
grep -Fq "exact staged bundle does not exist" "${temporary}/wrong-hash.log"
test "$(wc -l <"$curl_log")" = "$curl_calls_before"
test ! -e "$active"

chmod g+w "${second_release}/manifest.json"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/writable-target.log" 2>&1; then
  echo "activate accepted a group-writable target bundle file" >&2
  exit 1
fi
grep -Fq "immutable bundle entry manifest.json must not be group- or world-writable" "${temporary}/writable-target.log"
test ! -e "$active"
chmod g-w "${second_release}/manifest.json"

# Stage is allowed beside a legacy flat path; activation is not. The operator
# must use a separate V2 runtime root, and no synthetic previous link appears.
legacy="${temporary}/legacy/saiai-cli"
mkdir -p "$legacy"
chmod 0755 "$(dirname "$legacy")"
printf 'keep\n' >"${legacy}/sentinel"
run_for "$legacy" stage saiai-v0.9.1 "$second_hash" >"${temporary}/legacy-stage.log" 2>&1
test -f "${legacy}.releases/saiai-v0.9.1/${second_hash}/manifest.json"
grep -Fq "keep" "${legacy}/sentinel"
if SAIAI_TEST_OFFLINE=1 run_for "$legacy" activate saiai-v0.9.1 "$second_hash" >"${temporary}/legacy-activate.log" 2>&1; then
  echo "activate replaced a legacy flat directory" >&2
  exit 1
fi
grep -Fq "legacy flat directory or file" "${temporary}/legacy-activate.log"
grep -Fq "independent V2 runtime root" "${temporary}/legacy-activate.log"
grep -Fq "keep" "${legacy}/sentinel"
test ! -L "$legacy"
test ! -e "${legacy}.previous"

# A valid-looking active symlink may not import a foreign or broken directory.
foreign_active="${temporary}/foreign-runtime/saiai-cli"
run_for "$foreign_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/foreign-stage.log" 2>&1
mkdir -p "${temporary}/foreign-bundle"
ln -s "${temporary}/foreign-bundle" "$foreign_active"
if SAIAI_TEST_OFFLINE=1 run_for "$foreign_active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/foreign-active.log" 2>&1; then
  echo "activate accepted a foreign active symlink" >&2
  exit 1
fi
grep -Fq "must target an exact staged V2 bundle" "${temporary}/foreign-active.log"
rm "$foreign_active"
ln -s "${temporary}/missing-active-target" "$foreign_active"
if SAIAI_TEST_OFFLINE=1 run_for "$foreign_active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/broken-active.log" 2>&1; then
  echo "activate accepted a broken active symlink" >&2
  exit 1
fi
grep -Fq "symlink is broken" "${temporary}/broken-active.log"

# With no active link, any previous entry (valid or broken) is stale state.
stale_active="${temporary}/stale-runtime/saiai-cli"
run_for "$stale_active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/stale-stage.log" 2>&1
stale_release="${stale_active}.releases/saiai-v0.9.1/${second_hash}"
ln -s "$stale_release" "${stale_active}.previous"
if SAIAI_TEST_OFFLINE=1 run_for "$stale_active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/stale-valid-previous.log" 2>&1; then
  echo "first activation accepted a stale previous symlink" >&2
  exit 1
fi
grep -Fq "first V2 activation requires both active and previous links to be absent" "${temporary}/stale-valid-previous.log"
rm "${stale_active}.previous"
ln -s "${temporary}/missing-previous-target" "${stale_active}.previous"
if SAIAI_TEST_OFFLINE=1 run_for "$stale_active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/stale-broken-previous.log" 2>&1; then
  echo "first activation accepted a broken previous symlink" >&2
  exit 1
fi
grep -Fq "first V2 activation requires both active and previous links to be absent" "${temporary}/stale-broken-previous.log"

# First activation is offline and creates no imaginary previous release.
curl_calls_before="$(wc -l <"$curl_log")"
SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.0 "$first_hash" >"${temporary}/first-activate.log" 2>&1
test "$(wc -l <"$curl_log")" = "$curl_calls_before"
grep -Fq "previous:      none (first V2 activation)" "${temporary}/first-activate.log"
test -L "$active"
test "$(readlink -f "$active")" = "$first_release"
test ! -e "${active}.previous"

# Existing live state is revalidated before it can become the rollback target.
chmod g+w "${first_release}/saiai-linux-x86_64"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/writable-active.log" 2>&1; then
  echo "activate accepted a group-writable current bundle file" >&2
  exit 1
fi
grep -Fq "immutable bundle entry saiai-linux-x86_64 must not be group- or world-writable" "${temporary}/writable-active.log"
test "$(readlink -f "$active")" = "$first_release"
test ! -e "${active}.previous"
chmod g-w "${first_release}/saiai-linux-x86_64"

chmod u+w "${first_release}/saiai-linux-x86_64"
printf 'tampered-active\n' >>"${first_release}/saiai-linux-x86_64"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/corrupt-active.log" 2>&1; then
  echo "activate trusted a corrupted current bundle" >&2
  exit 1
fi
grep -Fq "mismatch for saiai-linux-x86_64" "${temporary}/corrupt-active.log"
test "$(readlink -f "$active")" = "$first_release"
test ! -e "${active}.previous"
chmod u+w "${first_release}/saiai-linux-x86_64"
cp "${fixtures}/releases/saiai-v0.9.0/saiai-linux-x86_64" "${first_release}/saiai-linux-x86_64"

# A foreign or broken previous link is rejected instead of silently overwritten.
ln -s "${temporary}/foreign-bundle" "${active}.previous"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/foreign-previous.log" 2>&1; then
  echo "activate accepted a foreign previous symlink" >&2
  exit 1
fi
grep -Fq "must target an exact staged V2 bundle" "${temporary}/foreign-previous.log"
rm "${active}.previous"
ln -s "${temporary}/missing-previous-target" "${active}.previous"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/broken-previous.log" 2>&1; then
  echo "activate accepted a broken previous symlink" >&2
  exit 1
fi
grep -Fq "symlink is broken" "${temporary}/broken-previous.log"
rm "${active}.previous"

# Activation re-applies permissions before doing its offline verification.
chmod 0755 "$(dirname "$second_release")" "$second_release"
chmod 0644 "${second_release}/saiai-linux-x86_64" "${second_release}/setup.sh"
chmod 0755 "${second_release}/manifest.json"
SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/second-activate.log" 2>&1
test "$(readlink -f "$active")" = "$second_release"
test -L "${active}.previous"
test "$(readlink -f "${active}.previous")" = "$first_release"
test "$(stat -c '%a' "$(dirname "$second_release")")" = "555"
test "$(stat -c '%a' "$second_release")" = "555"
test "$(stat -c '%a' "${second_release}/saiai-linux-x86_64")" = "555"
test "$(stat -c '%a' "${second_release}/setup.sh")" = "555"
test "$(stat -c '%a' "${second_release}/manifest.json")" = "444"

# Previous is also revalidated on every activation.
chmod g+w "${first_release}/saiai-linux-x86_64"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/writable-previous.log" 2>&1; then
  echo "activate accepted a group-writable previous bundle file" >&2
  exit 1
fi
grep -Fq "immutable bundle entry saiai-linux-x86_64 must not be group- or world-writable" "${temporary}/writable-previous.log"
test "$(readlink -f "$active")" = "$second_release"
test "$(readlink -f "${active}.previous")" = "$first_release"
chmod g-w "${first_release}/saiai-linux-x86_64"

chmod u+w "${first_release}/saiai-linux-x86_64"
printf 'tampered-previous\n' >>"${first_release}/saiai-linux-x86_64"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.1 "$second_hash" >"${temporary}/corrupt-previous.log" 2>&1; then
  echo "activate trusted a corrupted previous bundle" >&2
  exit 1
fi
grep -Fq "mismatch for saiai-linux-x86_64" "${temporary}/corrupt-previous.log"
test "$(readlink -f "$active")" = "$second_release"
test "$(readlink -f "${active}.previous")" = "$first_release"
chmod u+w "${first_release}/saiai-linux-x86_64"
cp "${fixtures}/releases/saiai-v0.9.0/saiai-linux-x86_64" "${first_release}/saiai-linux-x86_64"

# A locally corrupted staged bundle is rejected offline and cannot move links.
run_for "$active" stage saiai-v0.9.3 "$local_corrupt_hash" >"${temporary}/local-corrupt-stage.log" 2>&1
local_corrupt_release="${active}.releases/saiai-v0.9.3/${local_corrupt_hash}"
chmod u+w "${local_corrupt_release}/saiai-linux-x86_64"
printf 'locally-tampered\n' >>"${local_corrupt_release}/saiai-linux-x86_64"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.3 "$local_corrupt_hash" >"${temporary}/local-corrupt-activate.log" 2>&1; then
  echo "activate accepted a locally corrupt staged bundle" >&2
  exit 1
fi
grep -Fq "mismatch for saiai-linux-x86_64" "${temporary}/local-corrupt-activate.log"
test "$(readlink -f "$active")" = "$second_release"
test "$(readlink -f "${active}.previous")" = "$first_release"

# GitHub immutable=true satisfies fail-closed staging.
SAIAI_REQUIRE_IMMUTABLE_RELEASE=1 run_for "$active" stage saiai-v0.9.4 "$immutable_hash" >"${temporary}/immutable-stage.log" 2>&1
grep -Fq "GitHub immutable: true" "${temporary}/immutable-stage.log"
test -d "${active}.releases/saiai-v0.9.4/${immutable_hash}"
test "$(readlink -f "$active")" = "$second_release"

# Model an interruption after previous was atomically updated but before active
# changed. The pair is not one transaction; rerunning completes it safely.
immutable_release="${active}.releases/saiai-v0.9.4/${immutable_hash}"
rm "${active}.previous"
ln -s "$second_release" "${active}.previous"
SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.4 "$immutable_hash" >"${temporary}/resume-activate.log" 2>&1
test "$(readlink -f "$active")" = "$immutable_release"
test "$(readlink -f "${active}.previous")" = "$second_release"

# The public release itself must contain exactly the ten contract assets.
python3 - "${fixtures}/releases/saiai-v0.9.4/release.json" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
release = json.loads(path.read_text(encoding="utf-8"))
release["assets"].append({"id": 999, "name": "unexpected-notes.txt"})
path.write_text(json.dumps(release), encoding="utf-8")
PY
if run_for "$active" stage saiai-v0.9.4 "$immutable_hash" >"${temporary}/extra-asset.log" 2>&1; then
  echo "stage accepted a release with an extra asset" >&2
  exit 1
fi
grep -Fq "release contains unexpected files: unexpected-notes.txt" "${temporary}/extra-asset.log"
test "$(readlink -f "$active")" = "$immutable_release"

# Both operations for the fixed canonical runtime identity use one non-blocking
# lock derived from its releases directory.
exec 8>>"${active}.releases.lock"
flock -n 8
if run_for "$active" stage saiai-v0.9.1 "$second_hash" >"${temporary}/locked-stage.log" 2>&1; then
  echo "stage ignored the shared lock" >&2
  exit 1
fi
grep -Fq "another SAIAI client stage or activation" "${temporary}/locked-stage.log"
if SAIAI_TEST_OFFLINE=1 run_for "$active" activate saiai-v0.9.0 "$first_hash" >"${temporary}/locked-activate.log" 2>&1; then
  echo "activate ignored the shared lock" >&2
  exit 1
fi
grep -Fq "another SAIAI client stage or activation" "${temporary}/locked-activate.log"

echo "SAIAI two-phase immutable release checks passed"
