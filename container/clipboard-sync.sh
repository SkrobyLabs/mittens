#!/usr/bin/env bash
# Polls macOS clipboard for images, saves to shared directory.
# Started by mittens, killed on exit.
set -euo pipefail

CLIP_DIR="$1"
CLIP_FILE="${CLIP_DIR}/clipboard.png"
TMP_FILE="${CLIP_DIR}/clipboard.png.tmp"
PID_FILE="${CLIP_DIR}/clipboard-sync.pid"
STATE_FILE="${CLIP_DIR}/clipboard.state"
UPDATED_AT_FILE="${CLIP_DIR}/clipboard.updated_at"
HEARTBEAT_FILE="${CLIP_DIR}/clipboard.heartbeat"
ERROR_FILE="${CLIP_DIR}/clipboard.error"
CLIENTS_DIR="${CLIP_DIR}/clients"
LAST_HASH=""
FAILURES=0

mkdir -p "${CLIENTS_DIR}"
printf '%s\n' "$$" > "${PID_FILE}"
trap 'rm -f "${PID_FILE}" "${TMP_FILE}"' EXIT

write_epoch() {
    local target="$1"
    printf '%s\n' "$(date +%s)" > "${target}"
}

install_file_atomic() {
    local src="$1" dst="$2" tmp
    tmp="${dst}.tmp.$$"
    cp "${src}" "${tmp}"
    mv "${tmp}" "${dst}"
}

sync_client_dirs() {
    local reg_file target
    shopt -s nullglob
    for reg_file in "${CLIENTS_DIR}"/*.path; do
        target="$(tr -d '\r\n' < "${reg_file}" 2>/dev/null || true)"
        if [[ -z "${target}" || ! -d "${target}" ]]; then
            rm -f "${reg_file}"
            continue
        fi

        if [[ -f "${STATE_FILE}" ]]; then
            install_file_atomic "${STATE_FILE}" "${target}/clipboard.state"
        fi
        if [[ -f "${ERROR_FILE}" ]]; then
            install_file_atomic "${ERROR_FILE}" "${target}/clipboard.error"
        fi
        if [[ -f "${UPDATED_AT_FILE}" ]]; then
            install_file_atomic "${UPDATED_AT_FILE}" "${target}/clipboard.updated_at"
        else
            rm -f "${target}/clipboard.updated_at"
        fi
        if [[ -f "${CLIP_FILE}" ]]; then
            install_file_atomic "${CLIP_FILE}" "${target}/clipboard.png"
        else
            rm -f "${target}/clipboard.png"
        fi
    done
    shopt -u nullglob
}

while true; do
    sleep 1
    write_epoch "${HEARTBEAT_FILE}"

    RESULT="$(
        osascript -e '
            try
                set png_data to (the clipboard as «class PNGf»)
                set fp to open for access POSIX file "'"${TMP_FILE}"'" with write permission
                set eof of fp to 0
                write png_data to fp
                close access fp
                return "image"
            on error errMsg number errNum
                return "error:" & errNum & ":" & errMsg
            end try
        '
    )"

    if [[ "${RESULT}" != "image" ]]; then
        FAILURES=$((FAILURES + 1))
        printf '%s\n' "${RESULT#error:}" > "${ERROR_FILE}"
        printf '%s\n' "error" > "${STATE_FILE}"
        rm -f "${TMP_FILE}"
        sync_client_dirs
        if [[ "${FAILURES}" -eq 1 || $((FAILURES % 10)) -eq 0 ]]; then
            echo "[mittens] clipboard sync miss (${FAILURES}): ${RESULT#error:}" >&2
        fi
        continue
    fi

    FAILURES=0
    : > "${ERROR_FILE}"
    printf '%s\n' "image" > "${STATE_FILE}"

    # Only update if clipboard image changed
    if [[ -f "${TMP_FILE}" ]]; then
        NEW_HASH=$(md5 -q "${TMP_FILE}" 2>/dev/null)
        if [[ -n "$NEW_HASH" && "$NEW_HASH" != "$LAST_HASH" ]]; then
            mv "${TMP_FILE}" "${CLIP_FILE}"
            LAST_HASH="$NEW_HASH"
        else
            rm -f "${TMP_FILE}"
        fi
        write_epoch "${UPDATED_AT_FILE}"
    fi
    sync_client_dirs
done
