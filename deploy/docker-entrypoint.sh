#!/bin/sh
set -e

# Fix data directory permissions when running as root.
# Docker named volumes / host bind-mounts may be owned by root,
# preventing the non-root sub2api user from writing files.
if [ "$(id -u)" = "0" ]; then
    mkdir -p /app/data
    # Use || true to avoid failure on read-only mounted files (e.g. config.yaml:ro)
    chown -R sub2api:sub2api /app/data 2>/dev/null || true
    if [ -n "${EXTRA_CA_CERT_FILE:-}" ] && [ -f "${EXTRA_CA_CERT_FILE}" ]; then
        install -d /usr/local/share/ca-certificates
        cp "${EXTRA_CA_CERT_FILE}" /usr/local/share/ca-certificates/extra-dev-ca.crt
        chmod 0644 /usr/local/share/ca-certificates/extra-dev-ca.crt
        update-ca-certificates >/dev/null 2>&1 || true
    fi
    # Re-invoke this script as sub2api so the flag-detection below
    # also runs under the correct user.
    exec su-exec sub2api "$0" "$@"
fi

# Compatibility: if the first arg looks like a flag (e.g. --help),
# prepend the default binary so it behaves the same as the old
# ENTRYPOINT ["/app/sub2api"] style.
if [ "${1#-}" != "$1" ]; then
    set -- /app/sub2api "$@"
fi

exec "$@"
