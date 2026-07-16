#!/usr/bin/env bash
# Stage or activate one immutable SAIAI client GitHub Release.
#
# Usage:
#   scripts/sync-saiai-cli.sh stage saiai-v1.1.0 <manifest-sha256>
#   scripts/sync-saiai-cli.sh activate saiai-v1.1.0 <manifest-sha256>
#
# `stage` is the only networked operation. It downloads and verifies the full
# release into an immutable local bundle without changing the live symlinks.
# `activate` is deliberately offline: it re-verifies one exact staged bundle,
# then replaces each live symlink atomically under the shared release-root lock.
#
# Environment:
#   GH_TOKEN                       Optional token for the default GitHub API only
#   SAIAI_GH_REPO                  Release repository (default: yuns2023/saiai-client)
#   SAIAI_GITHUB_API_URL           GitHub API base (default: https://api.github.com)
#   SAIAI_REQUIRE_IMMUTABLE_RELEASE
#                                  Set to 1 to require GitHub immutable=true
#   SAIAI_CLIENT_DIR               Gateway-visible stable symlink
#                                  (default: /var/lib/saiai-server/client-runtime/saiai-cli)
#   SAIAI_CLIENT_RELEASES_DIR      Immutable bundle directory
#                                  (must equal <SAIAI_CLIENT_DIR>.releases)
#   SAIAI_CLIENT_PREVIOUS_LINK     Previous-bundle symlink
#                                  (must equal <SAIAI_CLIENT_DIR>.previous)

set -euo pipefail
umask 022

readonly DEFAULT_REPO="yuns2023/saiai-client"
readonly DEFAULT_CLIENT_LINK="/var/lib/saiai-server/client-runtime/saiai-cli"
readonly DEFAULT_GITHUB_API_URL="https://api.github.com"
readonly MAX_RELEASE_METADATA_BYTES=4194304
readonly MAX_ASSET_BYTES=67108864

readonly -a BINARIES=(
  saiai-linux-x86_64
  saiai-linux-aarch64
  saiai-macos-x86_64
  saiai-macos-aarch64
  saiai-windows-x86_64.exe
  saiai-windows-aarch64.exe
)
readonly -a WRAPPERS=(setup.sh setup.ps1 setup.cmd)
readonly -a RELEASE_FILES=(manifest.json "${BINARIES[@]}" "${WRAPPERS[@]}")

usage() {
  echo "Usage:" >&2
  echo "  $0 stage saiai-v<version> <manifest-sha256>" >&2
  echo "  $0 activate saiai-v<version> <manifest-sha256>" >&2
  echo "An explicit release tag and independently recorded exact manifest hash are required; 'latest' is not supported." >&2
}

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

[ "$#" -ge 1 ] || {
  usage
  exit 2
}

ACTION="$1"
case "$ACTION" in
  stage)
    [ "$#" -eq 3 ] || {
      usage
      exit 2
    }
    TAG="$2"
    REQUESTED_MANIFEST_SHA256="$3"
    ;;
  activate)
    [ "$#" -eq 3 ] || {
      usage
      exit 2
    }
    TAG="$2"
    REQUESTED_MANIFEST_SHA256="$3"
    ;;
  *)
    usage
    fail "unknown operation: $ACTION"
    ;;
esac

if [[ ! "$TAG" =~ ^saiai-v[0-9]+\.[0-9]+\.[0-9]+([.+-][0-9A-Za-z][0-9A-Za-z.+-]*)?$ ]]; then
  usage
  fail "invalid release tag: $TAG"
fi
if [[ ! "$REQUESTED_MANIFEST_SHA256" =~ ^[0-9a-f]{64}$ ]]; then
  usage
  fail "manifest sha256 must be exactly 64 lowercase hexadecimal characters"
fi

REPO="${SAIAI_GH_REPO:-$DEFAULT_REPO}"
if [[ ! "$REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  fail "invalid SAIAI_GH_REPO: $REPO"
fi
GITHUB_API_URL="${SAIAI_GITHUB_API_URL:-$DEFAULT_GITHUB_API_URL}"
GITHUB_API_URL="${GITHUB_API_URL%/}"
if [ -n "${GH_TOKEN:-}" ] && [ "$GITHUB_API_URL" != "$DEFAULT_GITHUB_API_URL" ]; then
  fail "GH_TOKEN may only be sent to $DEFAULT_GITHUB_API_URL; unset GH_TOKEN when using a custom test API"
fi
REQUIRE_IMMUTABLE_RELEASE="${SAIAI_REQUIRE_IMMUTABLE_RELEASE:-0}"
case "$REQUIRE_IMMUTABLE_RELEASE" in
  0|1) ;;
  *) fail "SAIAI_REQUIRE_IMMUTABLE_RELEASE must be 0 or 1" ;;
esac

for command_name in flock python3; do
  command -v "$command_name" >/dev/null 2>&1 || fail "$command_name is required"
done
if [ "$ACTION" = "stage" ]; then
  command -v curl >/dev/null 2>&1 || fail "curl is required for stage"
fi

absolute_sibling_path() {
  local raw_path="$1"
  local parent base
  parent="$(dirname -- "$raw_path")"
  base="$(basename -- "$raw_path")"
  [ "$base" != "." ] && [ "$base" != ".." ] && [ "$base" != "/" ] && [ -n "$base" ] || fail "invalid path: $raw_path"
  mkdir -p -- "$parent"
  parent="$(cd -- "$parent" && pwd -P)"
  printf '%s/%s\n' "$parent" "$base"
}

validate_trusted_entry() {
  local path="$1"
  local expected_kind="$2"
  local label="$3"
  python3 - "$path" "$expected_kind" "$label" <<'PY'
import os
import stat
import sys

path, expected_kind, label = sys.argv[1:]
try:
    details = os.lstat(path)
except OSError as exc:
    raise SystemExit(f"{label} cannot be inspected: {exc}") from exc
if expected_kind == "directory":
    valid_kind = stat.S_ISDIR(details.st_mode) and not stat.S_ISLNK(details.st_mode)
elif expected_kind == "file":
    valid_kind = stat.S_ISREG(details.st_mode)
else:
    raise SystemExit(f"internal error: unsupported trusted entry kind {expected_kind!r}")
if not valid_kind:
    raise SystemExit(f"{label} is not a real {expected_kind}: {path}")
if details.st_uid != os.geteuid():
    raise SystemExit(
        f"{label} must be owned by the current euid {os.geteuid()}, got uid {details.st_uid}: {path}"
    )
if stat.S_IMODE(details.st_mode) & 0o022:
    raise SystemExit(f"{label} must not be group- or world-writable: {path}")
PY
}

validate_trusted_bundle_tree() {
  local root="$1"
  local label="$2"
  python3 - "$root" "$label" "${RELEASE_FILES[@]}" <<'PY'
import os
import stat
import sys

root, label, *required = sys.argv[1:]
expected_uid = os.geteuid()

def inspect(path, expected_kind, entry_label):
    try:
        details = os.lstat(path)
    except OSError as exc:
        raise SystemExit(f"{entry_label} cannot be inspected: {exc}") from exc
    if expected_kind == "directory":
        valid_kind = stat.S_ISDIR(details.st_mode) and not stat.S_ISLNK(details.st_mode)
    else:
        valid_kind = stat.S_ISREG(details.st_mode)
    if not valid_kind:
        raise SystemExit(f"{entry_label} is not a real {expected_kind}: {path}")
    if details.st_uid != expected_uid:
        raise SystemExit(
            f"{entry_label} must be owned by the current euid {expected_uid}, "
            f"got uid {details.st_uid}: {path}"
        )
    if stat.S_IMODE(details.st_mode) & 0o022:
        raise SystemExit(f"{entry_label} must not be group- or world-writable: {path}")

inspect(root, "directory", label)
try:
    actual = os.listdir(root)
except OSError as exc:
    raise SystemExit(f"{label} cannot be listed: {exc}") from exc
if set(actual) != set(required) or len(actual) != len(required):
    raise SystemExit(
        f"immutable bundle file set differs: expected {sorted(required)}, got {sorted(actual)}"
    )
for name in required:
    inspect(os.path.join(root, name), "file", f"{label} entry {name}")
PY
}

ACTIVE_LINK="$(absolute_sibling_path "${SAIAI_CLIENT_DIR:-$DEFAULT_CLIENT_LINK}")"
RELEASES_DIR="$(absolute_sibling_path "${SAIAI_CLIENT_RELEASES_DIR:-${ACTIVE_LINK}.releases}")"
PREVIOUS_LINK="$(absolute_sibling_path "${SAIAI_CLIENT_PREVIOUS_LINK:-${ACTIVE_LINK}.previous}")"
RUNTIME_PARENT="$(dirname -- "$ACTIVE_LINK")"
LOCK_FILE="${RELEASES_DIR}.lock"

[ "$ACTIVE_LINK" != "$RELEASES_DIR" ] || fail "SAIAI_CLIENT_DIR and SAIAI_CLIENT_RELEASES_DIR must differ"
[ "$ACTIVE_LINK" != "$PREVIOUS_LINK" ] || fail "SAIAI_CLIENT_DIR and SAIAI_CLIENT_PREVIOUS_LINK must differ"
[ "$RELEASES_DIR" != "$PREVIOUS_LINK" ] || fail "SAIAI_CLIENT_RELEASES_DIR and SAIAI_CLIENT_PREVIOUS_LINK must differ"
[ "$ACTIVE_LINK" != "$LOCK_FILE" ] || fail "SAIAI_CLIENT_DIR conflicts with the shared release lock"
[ "$PREVIOUS_LINK" != "$LOCK_FILE" ] || fail "SAIAI_CLIENT_PREVIOUS_LINK conflicts with the shared release lock"
[ "$(dirname -- "$RELEASES_DIR")" = "$RUNTIME_PARENT" ] || fail "SAIAI client links and releases must share one runtime parent"
[ "$(dirname -- "$PREVIOUS_LINK")" = "$RUNTIME_PARENT" ] || fail "SAIAI client links and releases must share one runtime parent"
[ "$RELEASES_DIR" = "${ACTIVE_LINK}.releases" ] || fail "SAIAI_CLIENT_RELEASES_DIR must use the canonical path ${ACTIVE_LINK}.releases"
[ "$PREVIOUS_LINK" = "${ACTIVE_LINK}.previous" ] || fail "SAIAI_CLIENT_PREVIOUS_LINK must use the canonical path ${ACTIVE_LINK}.previous"

# Runtime client files are public downloads, but only the current deployment
# euid may mutate the dedicated runtime parent and release root.
validate_trusted_entry "$RUNTIME_PARENT" directory "SAIAI runtime parent"
chmod a+rx -- "$RUNTIME_PARENT"
if [ "$ACTION" = "stage" ]; then
  if [ -e "$RELEASES_DIR" ] || [ -L "$RELEASES_DIR" ]; then
    [ -d "$RELEASES_DIR" ] && [ ! -L "$RELEASES_DIR" ] || fail "SAIAI_CLIENT_RELEASES_DIR is not a real directory: $RELEASES_DIR"
  else
    mkdir -m 0755 -- "$RELEASES_DIR"
  fi
  validate_trusted_entry "$RELEASES_DIR" directory "SAIAI client releases root"
  chmod 0755 -- "$RELEASES_DIR"
else
  [ -d "$RELEASES_DIR" ] && [ ! -L "$RELEASES_DIR" ] || fail "no staged release root exists at $RELEASES_DIR"
  validate_trusted_entry "$RELEASES_DIR" directory "SAIAI client releases root"
  chmod 0755 -- "$RELEASES_DIR"
fi

if [ -e "$LOCK_FILE" ] || [ -L "$LOCK_FILE" ]; then
  [ ! -L "$LOCK_FILE" ] || fail "shared release lock must not be a symlink: $LOCK_FILE"
  validate_trusted_entry "$LOCK_FILE" file "SAIAI shared release lock"
fi
exec 9>>"$LOCK_FILE"
chmod 0600 -- "$LOCK_FILE"
validate_trusted_entry "$LOCK_FILE" file "SAIAI shared release lock"
if ! flock -n 9; then
  fail "another SAIAI client stage or activation holds $LOCK_FILE"
fi

STAGE_DIR=""
TAG_RELEASES_DIR="${RELEASES_DIR}/${TAG}"
TAG_DIR_TOUCHED=0
cleanup() {
  if [ -n "$STAGE_DIR" ] && [ -d "$STAGE_DIR" ] && [ ! -L "$STAGE_DIR" ]; then
    chmod -R u+rwX "$STAGE_DIR" 2>/dev/null || true
    rm -rf -- "$STAGE_DIR"
  fi
  if [ "$TAG_DIR_TOUCHED" -eq 1 ] && [ -d "$TAG_RELEASES_DIR" ] && [ ! -L "$TAG_RELEASES_DIR" ]; then
    chmod 0555 -- "$TAG_RELEASES_DIR" 2>/dev/null || true
  fi
}
trap cleanup EXIT

validate_bundle_shape_for_chmod() {
  local root="$1"
  validate_trusted_bundle_tree "$root" "immutable bundle"
  chmod 0755 -- "$root"
}

restore_bundle_permissions() {
  local root="$1"
  validate_bundle_shape_for_chmod "$root"
  local binary
  for binary in "${BINARIES[@]}"; do
    chmod 0555 -- "${root}/${binary}"
  done
  chmod 0555 -- "${root}/setup.sh"
  chmod 0444 -- "${root}/setup.ps1" "${root}/setup.cmd" "${root}/manifest.json"
  chmod 0555 -- "$root"
  validate_trusted_bundle_tree "$root" "immutable bundle"
}

validate_bundle() {
  local root="$1"
  local expected_version="$2"
  local expected_manifest_sha256="$3"
  local expected_contract="${4:-local-proxy}"
  python3 - "$root" "$expected_version" "$expected_manifest_sha256" "$MAX_ASSET_BYTES" "$expected_contract" "${BINARIES[@]}" -- "${WRAPPERS[@]}" <<'PY'
import hashlib
import json
import os
import re
import stat
import sys

root = sys.argv[1]
expected_version = sys.argv[2]
expected_manifest_sha256 = sys.argv[3]
max_file_bytes = int(sys.argv[4])
expected_contract = sys.argv[5]
separator = sys.argv.index("--")
binaries = sys.argv[6:separator]
wrappers = sys.argv[separator + 1:]
required = ["manifest.json", *binaries, *wrappers]
manifest_path = os.path.join(root, "manifest.json")

def regular_file(path, label):
    try:
        mode = os.lstat(path).st_mode
    except OSError as exc:
        raise SystemExit(f"missing {label}: {exc}") from exc
    if not stat.S_ISREG(mode):
        raise SystemExit(f"{label} is not a regular file")

def digest(path):
    value = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()

try:
    actual_entries = os.listdir(root)
except OSError as exc:
    raise SystemExit(f"cannot inspect immutable bundle: {exc}") from exc
if set(actual_entries) != set(required) or len(actual_entries) != len(required):
    raise SystemExit(
        f"immutable bundle file set differs: expected {sorted(required)}, got {sorted(actual_entries)}"
    )

regular_file(manifest_path, "manifest.json")
manifest_size = os.path.getsize(manifest_path)
if manifest_size <= 0:
    raise SystemExit("manifest.json is empty")
if manifest_size > max_file_bytes:
    raise SystemExit(
        f"manifest.json exceeds the {max_file_bytes}-byte immutable bundle file limit"
    )
manifest_sha256 = digest(manifest_path)
if expected_manifest_sha256 != "-" and manifest_sha256 != expected_manifest_sha256:
    raise SystemExit(
        f"manifest sha256 mismatch: expected {expected_manifest_sha256}, got {manifest_sha256}"
    )
try:
    with open(manifest_path, "r", encoding="utf-8") as handle:
        manifest = json.load(handle)
except (OSError, UnicodeError, json.JSONDecodeError) as exc:
    raise SystemExit(f"invalid manifest.json: {exc}") from exc

if type(manifest.get("manifest_schema")) is not int or manifest.get("manifest_schema") != 1:
    raise SystemExit("release manifest_schema must be 1")
is_global_config = (
    manifest.get("client_mode") == "global-config"
    and type(manifest.get("configuration_schema_version")) is int
    and manifest.get("configuration_schema_version") == 1
    and "bootstrap_schema_version" not in manifest
)
is_local_proxy = (
    manifest.get("client_mode") == "local-proxy"
    and type(manifest.get("configuration_schema_version")) is int
    and manifest.get("configuration_schema_version") == 1
    and "bootstrap_schema_version" not in manifest
)
is_retained_v2 = (
    "client_mode" not in manifest
    and "configuration_schema_version" not in manifest
    and type(manifest.get("bootstrap_schema_version")) is int
    and manifest.get("bootstrap_schema_version") == 2
)
if expected_contract == "local-proxy":
    if not is_local_proxy:
        raise SystemExit("release must be local-proxy schema 1 without a bootstrap claim")
elif expected_contract == "retained-live":
    if not (is_local_proxy or is_global_config or is_retained_v2):
        raise SystemExit(
            "live bundle is neither local-proxy/global-config schema 1 nor retained V2 schema 2"
        )
else:
    raise SystemExit(f"unsupported internal bundle contract: {expected_contract}")
if manifest.get("version") != expected_version:
    raise SystemExit(
        f"manifest version differs from tag: expected {expected_version!r}, got {manifest.get('version')!r}"
    )

def validate_section(section_name, expected_names):
    section = manifest.get(section_name)
    if not isinstance(section, dict):
        raise SystemExit(f"manifest {section_name} must be an object")
    if set(section) != set(expected_names):
        raise SystemExit(
            f"manifest {section_name} set differs: expected {sorted(expected_names)}, got {sorted(section)}"
        )
    for name in expected_names:
        metadata = section.get(name)
        if not isinstance(metadata, dict):
            raise SystemExit(f"manifest metadata is invalid for {name}")
        expected_hash = metadata.get("sha256")
        expected_size = metadata.get("size")
        if not isinstance(expected_hash, str) or re.fullmatch(r"[0-9a-f]{64}", expected_hash) is None:
            raise SystemExit(f"manifest sha256 is invalid for {name}")
        if type(expected_size) is not int or expected_size <= 0 or expected_size > max_file_bytes:
            raise SystemExit(f"manifest size is invalid for {name}")
        path = os.path.join(root, name)
        regular_file(path, name)
        actual_size = os.path.getsize(path)
        if actual_size > max_file_bytes:
            raise SystemExit(f"{name} exceeds the {max_file_bytes}-byte immutable bundle file limit")
        if actual_size != expected_size:
            raise SystemExit(f"size mismatch for {name}: expected {expected_size}, got {actual_size}")
        actual_hash = digest(path)
        if actual_hash != expected_hash:
            raise SystemExit(f"sha256 mismatch for {name}: expected {expected_hash}, got {actual_hash}")

validate_section("assets", binaries)
validate_section("wrappers", wrappers)
print(manifest_sha256)
PY
}

verify_tag_directory() {
  local tag_directory="$1"
  local expected_hash="$2"
  python3 - "$tag_directory" "$expected_hash" <<'PY'
import os
import sys

tag_directory, expected_hash = sys.argv[1:]
unexpected = [name for name in os.listdir(tag_directory) if name != expected_hash]
if unexpected:
    raise SystemExit(
        f"release tag was already staged with different content in {tag_directory}; "
        f"refusing a mutable tag: {sorted(unexpected)}"
    )
PY
}

validate_live_bundle_link() {
  local link_path="$1"
  local label="$2"
  [ -L "$link_path" ] || fail "$label must be a symlink: $link_path"
  [ -d "$link_path" ] || fail "$label symlink is broken or does not target a directory: $link_path"

  local resolved parsed live_tag live_hash live_tag_dir
  resolved="$(cd -- "$link_path" && pwd -P)"
  parsed="$(python3 - "$link_path" "$resolved" "$RELEASES_DIR" "$label" <<'PY'
import os
import re
import stat
import sys

link_path, resolved, releases_root, label = sys.argv[1:]
try:
    link_details = os.lstat(link_path)
except OSError as exc:
    raise SystemExit(f"{label} cannot be inspected: {exc}") from exc
if not stat.S_ISLNK(link_details.st_mode):
    raise SystemExit(f"{label} must be a symlink: {link_path}")
if link_details.st_uid != os.geteuid():
    raise SystemExit(
        f"{label} symlink must be owned by the current euid {os.geteuid()}, "
        f"got uid {link_details.st_uid}: {link_path}"
    )

relative = os.path.relpath(resolved, releases_root)
parts = relative.split(os.sep)
tag_pattern = re.compile(r"saiai-v[0-9]+\.[0-9]+\.[0-9]+(?:[.+-][0-9A-Za-z][0-9A-Za-z.+-]*)?")
hash_pattern = re.compile(r"[0-9a-f]{64}")
if (
    len(parts) != 2
    or tag_pattern.fullmatch(parts[0]) is None
    or hash_pattern.fullmatch(parts[1]) is None
    or resolved != os.path.join(releases_root, *parts)
):
    raise SystemExit(
        f"{label} must target an exact staged client bundle under {releases_root}/<tag>/<manifest-sha>: "
        f"{resolved}"
    )
print(parts[0] + "\t" + parts[1])
PY
)"
  IFS=$'\t' read -r live_tag live_hash <<<"$parsed"
  live_tag_dir="${RELEASES_DIR}/${live_tag}"
  [ -d "$live_tag_dir" ] && [ ! -L "$live_tag_dir" ] || fail "$label release tag directory is not real: $live_tag_dir"
  [ -d "$resolved" ] && [ ! -L "$resolved" ] || fail "$label bundle directory is not real: $resolved"
  validate_trusted_entry "$live_tag_dir" directory "$label release tag directory"
  verify_tag_directory "$live_tag_dir" "$live_hash"
  restore_bundle_permissions "$resolved"
  # A contract cutover may start with exact retained global-config or V2
  # active/previous bundles. Validate those hashes and schemas without allowing
  # stage/activate targets to bypass the local-proxy candidate contract.
  validate_bundle "$resolved" "${live_tag#saiai-v}" "$live_hash" retained-live >/dev/null
  VALIDATED_LIVE_BUNDLE="$resolved"
}

atomic_symlink() {
  local destination="$1"
  local target="$2"
  local temporary="${destination}.next.$$.$RANDOM"
  rm -f -- "$temporary"
  ln -s -- "$target" "$temporary"
  if ! python3 - "$temporary" "$destination" <<'PY'
import os
import sys

os.replace(sys.argv[1], sys.argv[2])
PY
  then
    rm -f -- "$temporary"
    return 1
  fi
}

download_with_stream_limit() {
  local destination="$1"
  local byte_limit="$2"
  shift 2
  local partial="${destination}.part.$$.$RANDOM"
  rm -f -- "$partial"
  if ! curl -q --proto '=https' --proto-redir '=https' --max-filesize "$byte_limit" "$@" | \
    python3 -c '
import os
import sys

destination = sys.argv[1]
byte_limit = int(sys.argv[2])
written = 0
try:
    with open(destination, "xb") as output:
        while True:
            block = sys.stdin.buffer.read(1024 * 1024)
            if not block:
                break
            written += len(block)
            if written > byte_limit:
                raise RuntimeError(f"download exceeded the {byte_limit}-byte streaming limit")
            output.write(block)
except Exception as exc:
    try:
        os.unlink(destination)
    except FileNotFoundError:
        pass
    raise SystemExit(str(exc)) from exc
' "$partial" "$byte_limit"
  then
    rm -f -- "$partial"
    return 1
  fi
  mv -- "$partial" "$destination"
}

stage_release() {
  STAGE_DIR="$(mktemp -d "${RELEASES_DIR}/.stage.${TAG}.XXXXXX")"
  local release_json="${STAGE_DIR}/.release.json"
  local asset_map="${STAGE_DIR}/.asset-map"
  local release_info="${STAGE_DIR}/.release-info"
  local auth_header_file=""
  local -a api_headers=(
    -H "Accept: application/vnd.github+json"
    -H "X-GitHub-Api-Version: 2022-11-28"
  )
  local -a auth_headers=()
  if [ -n "${GH_TOKEN:-}" ]; then
    auth_header_file="${STAGE_DIR}/.github-auth-header"
    printf 'Authorization: Bearer %s\n' "$GH_TOKEN" >"$auth_header_file"
    chmod 0600 "$auth_header_file"
    auth_headers=(-H "@${auth_header_file}")
  fi

  echo "[stage 1/3] resolving $REPO release $TAG"
  download_with_stream_limit "$release_json" "$MAX_RELEASE_METADATA_BYTES" \
    -fsSL --retry 3 --connect-timeout 15 \
    "${api_headers[@]}" "${auth_headers[@]}" \
    "${GITHUB_API_URL}/repos/${REPO}/releases/tags/${TAG}"

  python3 - "$release_json" "$TAG" "$release_info" "${RELEASE_FILES[@]}" >"$asset_map" <<'PY'
import json
import sys

release_path, expected_tag, release_info_path, *required = sys.argv[1:]
with open(release_path, "r", encoding="utf-8") as handle:
    release = json.load(handle)

if release.get("tag_name") != expected_tag:
    raise SystemExit(f"release tag differs: expected {expected_tag!r}, got {release.get('tag_name')!r}")
if release.get("draft") is not False:
    raise SystemExit("refusing a draft or malformed GitHub release")
if release.get("prerelease") is not False:
    raise SystemExit("refusing a prerelease or malformed GitHub release")
immutable = release.get("immutable")
immutable_text = "true" if immutable is True else "false" if immutable is False else "unknown"
with open(release_info_path, "w", encoding="utf-8") as handle:
    handle.write(immutable_text + "\n")

assets = release.get("assets")
if not isinstance(assets, list):
    raise SystemExit("release assets must be an array")
by_name = {}
for asset in assets:
    if not isinstance(asset, dict):
        raise SystemExit("release contains malformed asset metadata")
    name = asset.get("name")
    asset_id = asset.get("id")
    if not isinstance(name, str) or type(asset_id) is not int or asset_id <= 0:
        raise SystemExit("release contains malformed asset metadata")
    if name in by_name:
        raise SystemExit(f"release contains duplicate asset name: {name}")
    by_name[name] = asset_id

missing = [name for name in required if name not in by_name]
unexpected = [name for name in by_name if name not in required]
if missing:
    raise SystemExit("release is missing required files: " + ", ".join(missing))
if unexpected:
    raise SystemExit("release contains unexpected files: " + ", ".join(sorted(unexpected)))
for name in required:
    print(f"{name}\t{by_name[name]}")
PY

  local release_immutable
  release_immutable="$(<"$release_info")"
  echo "  GitHub immutable: $release_immutable"
  if [ "$release_immutable" != "true" ]; then
    echo "WARNING: GitHub does not report this release as immutable; local tag/hash immutability checks remain enforced." >&2
    if [ "$REQUIRE_IMMUTABLE_RELEASE" = "1" ]; then
      fail "GitHub release $TAG is not marked immutable"
    fi
  fi

  echo "[stage 2/3] downloading the complete flat release"
  local name asset_id
  while IFS=$'\t' read -r name asset_id; do
    [ -n "$name" ] && [ -n "$asset_id" ] || fail "invalid release asset map"
    download_with_stream_limit "${STAGE_DIR}/${name}" "$MAX_ASSET_BYTES" \
      -fsSL --retry 3 --connect-timeout 15 \
      -H "Accept: application/octet-stream" \
      -H "X-GitHub-Api-Version: 2022-11-28" \
      "${auth_headers[@]}" \
      "${GITHUB_API_URL}/repos/${REPO}/releases/assets/${asset_id}"
  done <"$asset_map"

  rm -f -- "$release_json" "$asset_map" "$release_info"
  if [ -n "$auth_header_file" ]; then
    rm -f -- "$auth_header_file"
  fi

  echo "[stage 3/3] validating local-proxy manifest, hashes, and sizes"
  local version manifest_sha256
  version="${TAG#saiai-v}"
  validate_trusted_bundle_tree "$STAGE_DIR" "downloaded release bundle"
  manifest_sha256="$(validate_bundle "$STAGE_DIR" "$version" "$REQUESTED_MANIFEST_SHA256")"
  [ "$manifest_sha256" = "$REQUESTED_MANIFEST_SHA256" ] || fail "validated manifest hash differs unexpectedly"

  if [ -e "$TAG_RELEASES_DIR" ] || [ -L "$TAG_RELEASES_DIR" ]; then
    [ -d "$TAG_RELEASES_DIR" ] && [ ! -L "$TAG_RELEASES_DIR" ] || fail "local release tag path is not a real directory: $TAG_RELEASES_DIR"
    validate_trusted_entry "$TAG_RELEASES_DIR" directory "SAIAI release tag directory"
    chmod 0755 -- "$TAG_RELEASES_DIR"
  else
    mkdir -m 0755 -- "$TAG_RELEASES_DIR"
    validate_trusted_entry "$TAG_RELEASES_DIR" directory "SAIAI release tag directory"
  fi
  TAG_DIR_TOUCHED=1
  verify_tag_directory "$TAG_RELEASES_DIR" "$manifest_sha256"

  local release_dir="${TAG_RELEASES_DIR}/${manifest_sha256}"
  if [ -e "$release_dir" ] || [ -L "$release_dir" ]; then
    [ -d "$release_dir" ] && [ ! -L "$release_dir" ] || fail "immutable release path is not a real directory: $release_dir"
    restore_bundle_permissions "$release_dir"
    validate_bundle "$release_dir" "$version" "$manifest_sha256" >/dev/null
    for name in "${RELEASE_FILES[@]}"; do
      cmp -s -- "${STAGE_DIR}/${name}" "${release_dir}/${name}" || fail "existing immutable bundle differs: $name"
    done
    chmod -R u+rwX "$STAGE_DIR"
    rm -rf -- "$STAGE_DIR"
    STAGE_DIR=""
  else
    if ! mv -- "$STAGE_DIR" "$release_dir"; then
      ls -ld -- "$RELEASES_DIR" "$TAG_RELEASES_DIR" "$STAGE_DIR" >&2 || true
      fail "could not publish the validated bundle into the immutable tag directory"
    fi
    STAGE_DIR=""
    restore_bundle_permissions "$release_dir"
  fi
  chmod 0555 -- "$TAG_RELEASES_DIR"
  validate_trusted_entry "$TAG_RELEASES_DIR" directory "SAIAI release tag directory"

  echo "SAIAI client stage complete; active and previous links were not changed"
  echo "  staged:        $release_dir"
  echo "  manifest sha:  $manifest_sha256"
}

activate_release() {
  local active_present=0
  local previous_present=0
  if [ -e "$ACTIVE_LINK" ] || [ -L "$ACTIVE_LINK" ]; then
    active_present=1
    [ -L "$ACTIVE_LINK" ] || fail "SAIAI_CLIENT_DIR=$ACTIVE_LINK is a legacy flat directory or file. Use an independent client runtime root (set SAIAI_CLIENT_DIR and stage there) before activation; this script does not convert legacy state or manufacture a previous link from it."
  fi
  if [ -e "$PREVIOUS_LINK" ] || [ -L "$PREVIOUS_LINK" ]; then
    previous_present=1
  fi
  if [ "$active_present" -eq 0 ] && [ "$previous_present" -eq 1 ]; then
    fail "first client activation requires both active and previous links to be absent; refusing stale previous entry: $PREVIOUS_LINK"
  fi

  local current_release=""
  if [ "$active_present" -eq 1 ]; then
    validate_live_bundle_link "$ACTIVE_LINK" "active SAIAI client"
    current_release="$VALIDATED_LIVE_BUNDLE"
  fi
  if [ "$previous_present" -eq 1 ]; then
    validate_live_bundle_link "$PREVIOUS_LINK" "previous SAIAI client"
  fi

  [ -d "$TAG_RELEASES_DIR" ] && [ ! -L "$TAG_RELEASES_DIR" ] || fail "release tag has not been staged: $TAG"
  validate_trusted_entry "$TAG_RELEASES_DIR" directory "SAIAI release tag directory"
  TAG_DIR_TOUCHED=1
  chmod 0755 -- "$TAG_RELEASES_DIR"
  local release_dir="${TAG_RELEASES_DIR}/${REQUESTED_MANIFEST_SHA256}"
  [ -d "$release_dir" ] && [ ! -L "$release_dir" ] || fail "exact staged bundle does not exist: $release_dir"
  verify_tag_directory "$TAG_RELEASES_DIR" "$REQUESTED_MANIFEST_SHA256"
  restore_bundle_permissions "$release_dir"
  local actual_manifest_sha256
  actual_manifest_sha256="$(validate_bundle "$release_dir" "${TAG#saiai-v}" "$REQUESTED_MANIFEST_SHA256")"
  [ "$actual_manifest_sha256" = "$REQUESTED_MANIFEST_SHA256" ] || fail "validated manifest hash differs unexpectedly"
  chmod 0555 -- "$TAG_RELEASES_DIR"
  validate_trusted_entry "$TAG_RELEASES_DIR" directory "SAIAI release tag directory"

  echo "[activate] switching to $release_dir"
  if [ "$current_release" != "$release_dir" ]; then
    if [ -n "$current_release" ]; then
      # Each symlink replacement is atomic, but the pair is deliberately not a
      # single transaction. Publish the known-good rollback target first. If
      # interruption happens before the active replacement, rerunning the same
      # activation validates both links and safely completes the switch.
      atomic_symlink "$PREVIOUS_LINK" "$current_release"
    fi
    atomic_symlink "$ACTIVE_LINK" "$release_dir"
  fi

  echo "SAIAI client activation complete"
  echo "  current:       $ACTIVE_LINK -> $release_dir"
  if [ -L "$PREVIOUS_LINK" ]; then
    echo "  previous:      $PREVIOUS_LINK -> $(readlink -- "$PREVIOUS_LINK")"
  else
    echo "  previous:      none (first client activation)"
  fi
  echo "  manifest sha:  $REQUESTED_MANIFEST_SHA256"
}

case "$ACTION" in
  stage) stage_release ;;
  activate) activate_release ;;
esac
