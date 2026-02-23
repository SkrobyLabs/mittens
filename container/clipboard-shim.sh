#!/usr/bin/env bash
# xclip shim — reads from mittens clipboard sync directory.
CLIPBOARD_IMAGE="/tmp/mittens-clipboard/clipboard.png"

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

case "$TARGET" in
    TARGETS)
        [[ -f "$CLIPBOARD_IMAGE" ]] && echo "image/png"
        ;;
    image/*)
        [[ -f "$CLIPBOARD_IMAGE" ]] && cat "$CLIPBOARD_IMAGE" || exit 1
        ;;
    text/plain)
        echo ""
        ;;
    *)
        exit 1
        ;;
esac
