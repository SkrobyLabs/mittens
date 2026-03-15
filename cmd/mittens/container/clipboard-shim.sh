#!/usr/bin/env bash
# xclip shim — reads from mittens clipboard sync directory.
CLIPBOARD_IMAGE="/tmp/mittens-clipboard/clipboard.png"
CLIPBOARD_UPDATED_AT="/tmp/mittens-clipboard/clipboard.updated_at"
CLIPBOARD_MAX_AGE="${MITTENS_X11_CLIPBOARD_MAX_AGE_SECONDS:-5}"
CLIPBOARD_LOG="/tmp/mittens-clipboard-shim.log"

log() {
    echo "[$(date +%H:%M:%S)] $*" >> "$CLIPBOARD_LOG" 2>/dev/null
}

TARGET="" OUTPUT=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        -selection) shift 2 ;;
        -t) TARGET="$2"; shift 2 ;;
        -o) OUTPUT=true; shift ;;
        *) shift ;;
    esac
done

log "called: output=$OUTPUT target=$TARGET args=$*"

$OUTPUT || { log "not output mode, exit 0"; exit 0; }

has_fresh_clipboard_image() {
    local now updated age
    if [[ ! -f "$CLIPBOARD_IMAGE" ]]; then
        log "MISS: $CLIPBOARD_IMAGE does not exist"
        return 1
    fi
    if [[ ! -f "$CLIPBOARD_UPDATED_AT" ]]; then
        log "MISS: $CLIPBOARD_UPDATED_AT does not exist"
        return 1
    fi
    updated="$(tr -d '[:space:]' < "$CLIPBOARD_UPDATED_AT" 2>/dev/null)"
    if [[ ! "$updated" =~ ^[0-9]+$ ]]; then
        log "MISS: updated_at not numeric: '$updated'"
        return 1
    fi
    now="$(date +%s)"
    age=$((now - updated))
    if (( age > CLIPBOARD_MAX_AGE )); then
        log "MISS: image too old: age=${age}s > max=${CLIPBOARD_MAX_AGE}s (updated=$updated now=$now)"
        return 1
    fi
    log "HIT: fresh image, age=${age}s"
    return 0
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
        log "unknown target: $TARGET"
        exit 1
        ;;
esac
