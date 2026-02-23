#!/usr/bin/env bash
# Polls macOS clipboard for images, saves to shared directory.
# Started by mittens, killed on exit.
set -euo pipefail

CLIP_DIR="$1"
LAST_HASH=""

while true; do
    sleep 1
    # Try to save clipboard image to temp file
    osascript -e '
        try
            set png_data to (the clipboard as «class PNGf»)
            set fp to open for access POSIX file "'"${CLIP_DIR}/clipboard.png.tmp"'" with write permission
            set eof of fp to 0
            write png_data to fp
            close access fp
        end try
    ' 2>/dev/null || continue
    # Only update if clipboard image changed
    if [[ -f "${CLIP_DIR}/clipboard.png.tmp" ]]; then
        NEW_HASH=$(md5 -q "${CLIP_DIR}/clipboard.png.tmp" 2>/dev/null)
        if [[ -n "$NEW_HASH" && "$NEW_HASH" != "$LAST_HASH" ]]; then
            mv "${CLIP_DIR}/clipboard.png.tmp" "${CLIP_DIR}/clipboard.png"
            LAST_HASH="$NEW_HASH"
        else
            rm -f "${CLIP_DIR}/clipboard.png.tmp"
        fi
    fi
done
