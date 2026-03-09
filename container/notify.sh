#!/usr/bin/env bash
# notify.sh — send notification to host broker
# Usage: notify.sh <event> [message]
# Called by Claude Code hooks (Stop, Notification)

PORT="${MITTENS_BROKER_PORT:-}"
SOCK="${MITTENS_BROKER_SOCK:-}"
NAME="${MITTENS_CONTAINER_NAME:-unknown}"
EVENT="${1:-unknown}"
MESSAGE="${2:-}"

# Build curl command for broker access (TCP vs Unix socket).
if [[ -n "$SOCK" ]]; then
    BROKER_URL="http://broker"
    CURL_BROKER=(curl --unix-socket "$SOCK")
elif [[ -n "$PORT" ]]; then
    BROKER_URL="http://host.docker.internal:$PORT"
    CURL_BROKER=(curl --noproxy '*')
else
    exit 0
fi

PAYLOAD=$(jq -nc --arg c "$NAME" --arg e "$EVENT" --arg m "$MESSAGE" \
  '{container: $c, event: $e, message: $m}')

"${CURL_BROKER[@]}" -sf --max-time 2 \
  -X POST -H 'Content-Type: application/json' \
  -d "$PAYLOAD" "$BROKER_URL/notify" 2>/dev/null || true

exit 0
