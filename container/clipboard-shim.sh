#!/usr/bin/env bash
# xclip shim — reads from mittens clipboard sync directory.
CLIPBOARD_IMAGE="/tmp/mittens-clipboard/clipboard.png"
CLIPBOARD_UPDATED_AT="/tmp/mittens-clipboard/clipboard.updated_at"
CLIPBOARD_MAX_AGE="${MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS:-5}"

TARGET="" OUTPUT=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        -selection) shift 2 ;;
        -t) TARGET="$2"; shift 2 ;;
        -o) OUTPUT=true; shift ;;
        *) shift ;;
    esac
done

$OUTPUT || exit 0

has_fresh_clipboard_image() {
    local now updated age
    [[ -f "$CLIPBOARD_IMAGE" && -f "$CLIPBOARD_UPDATED_AT" ]] || return 1
    updated="$(tr -d '[:space:]' < "$CLIPBOARD_UPDATED_AT" 2>/dev/null)"
    [[ "$updated" =~ ^[0-9]+$ ]] || return 1
    now="$(date +%s)"
    age=$((now - updated))
    (( age <= CLIPBOARD_MAX_AGE ))
}

case "$TARGET" in
    TARGETS)
        has_fresh_clipboard_image && echo "image/png"
        ;;
    image/*)
        has_fresh_clipboard_image && cat "$CLIPBOARD_IMAGE" || exit 1
        ;;
    text/plain)
        echo ""
        ;;
    *)
        exit 1
        ;;
esac
