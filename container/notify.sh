#!/usr/bin/env bash
# notify.sh — send notification to host broker
# Usage: notify.sh <event> [message]
# Called by Claude Code hooks (Stop, Notification)

PORT="${MITTENS_BROKER_PORT:-}"
TOKEN="${MITTENS_BROKER_TOKEN:-}"
NAME="${MITTENS_CONTAINER_NAME:-unknown}"
EVENT="${1:-unknown}"
MESSAGE="${2:-}"

[[ -z "$PORT" ]] && exit 0

BROKER_URL="http://host.docker.internal:$PORT"
PAYLOAD=$(jq -nc --arg c "$NAME" --arg e "$EVENT" --arg m "$MESSAGE" \
  '{container: $c, event: $e, message: $m}')

if [[ -n "$TOKEN" ]]; then
  curl -sf --noproxy '*' --max-time 2 \
    -X POST -H 'Content-Type: application/json' -H "X-Mittens-Token: $TOKEN" \
    -d "$PAYLOAD" "$BROKER_URL/notify" 2>/dev/null || true
else
  curl -sf --noproxy '*' --max-time 2 \
    -X POST -H 'Content-Type: application/json' \
    -d "$PAYLOAD" "$BROKER_URL/notify" 2>/dev/null || true
fi

exit 0
