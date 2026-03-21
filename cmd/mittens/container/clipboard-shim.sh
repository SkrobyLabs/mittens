#!/usr/bin/env bash
# xclip shim — reads clipboard images on demand from the host broker.
CLIPBOARD_LOG="/tmp/mittens-clipboard-shim.log"

PORT="${MITTENS_BROKER_PORT:-}"
SOCK="${MITTENS_BROKER_SOCK:-}"
TOKEN="${MITTENS_BROKER_TOKEN:-}"

log() {
    echo "[$(date +%H:%M:%S)] $*" >> "$CLIPBOARD_LOG" 2>/dev/null
}

# Build curl command for broker access (TCP vs Unix socket).
if [[ -n "$SOCK" ]]; then
    BROKER_URL="http://broker"
    CURL_CMD=(curl --unix-socket "$SOCK")
elif [[ -n "$PORT" ]]; then
    BROKER_URL="http://host.docker.internal:$PORT"
    CURL_CMD=(curl --noproxy '*')
else
    log "no broker configured, exit 1"
    exit 1
fi

# Add token header if set.
if [[ -n "$TOKEN" ]]; then
    CURL_CMD+=(-H "X-Mittens-Token: $TOKEN")
fi

TARGET="" OUTPUT=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        -selection) shift 2 ;;
        -t|-target) TARGET="$2"; shift 2 ;;
        -o) OUTPUT=true; shift ;;
        *) shift ;;
    esac
done

log "called: output=$OUTPUT target=$TARGET"

$OUTPUT || { log "not output mode, exit 0"; exit 0; }

case "$TARGET" in
    TARGETS)
        # Check if broker has an image by making a HEAD-like request.
        HTTP_CODE=$("${CURL_CMD[@]}" -sf --max-time 5 -o /dev/null -w '%{http_code}' "$BROKER_URL/clipboard" 2>/dev/null)
        if [[ "$HTTP_CODE" == "200" ]]; then
            log "TARGETS: image available"
            echo "image/png"
        else
            log "TARGETS: no image (HTTP $HTTP_CODE)"
        fi
        ;;
    image/*)
        log "requesting clipboard image from broker"
        "${CURL_CMD[@]}" -sf --max-time 5 "$BROKER_URL/clipboard" 2>/dev/null
        RC=$?
        if [[ $RC -ne 0 ]]; then
            log "MISS: broker returned no image (curl exit $RC)"
            exit 1
        fi
        log "HIT: image received"
        ;;
    text/plain)
        echo ""
        ;;
    *)
        log "unknown target: $TARGET"
        exit 1
        ;;
esac
