#!/usr/bin/env bash
# Mirrors the synced PNG clipboard file into an X11 clipboard selection.
set -euo pipefail

CLIPBOARD_IMAGE="${1:-/tmp/mittens-clipboard/clipboard.png}"
XCLIP_BIN="${XCLIP_BIN:-/usr/local/bin/xclip-real}"
LAST_HASH=""
OWNER_PID=""

cleanup() {
    if [[ -n "${OWNER_PID}" ]] && kill -0 "${OWNER_PID}" 2>/dev/null; then
        kill "${OWNER_PID}" 2>/dev/null || true
        wait "${OWNER_PID}" 2>/dev/null || true
    fi
}

trap cleanup EXIT INT TERM

while true; do
    sleep 1

    [[ -f "${CLIPBOARD_IMAGE}" ]] || continue

    NEW_HASH="$(md5sum "${CLIPBOARD_IMAGE}" | awk '{print $1}')"
    [[ -n "${NEW_HASH}" ]] || continue
    [[ "${NEW_HASH}" != "${LAST_HASH}" ]] || continue

    if [[ -n "${OWNER_PID}" ]] && kill -0 "${OWNER_PID}" 2>/dev/null; then
        kill "${OWNER_PID}" 2>/dev/null || true
        wait "${OWNER_PID}" 2>/dev/null || true
    fi

    "${XCLIP_BIN}" -selection clipboard -t image/png -i "${CLIPBOARD_IMAGE}" >/dev/null 2>&1 &
    OWNER_PID=$!
    LAST_HASH="${NEW_HASH}"
done
