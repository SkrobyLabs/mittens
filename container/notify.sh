#!/usr/bin/env bash
# notify.sh — send notification to host broker
# Usage: notify.sh <event> [message]
# Called by Claude Code hooks (Stop, Notification)

PORT="${MITTENS_BROKER_PORT:-}"
NAME="${MITTENS_CONTAINER_NAME:-unknown}"
EVENT="${1:-unknown}"
MESSAGE="${2:-}"

[[ -z "$PORT" ]] && exit 0

BROKER_URL="http://host.docker.internal:$PORT"
PAYLOAD=$(printf '{"container":"%s","event":"%s","message":"%s"}' "$NAME" "$EVENT" "$MESSAGE")

curl -sf --noproxy '*' --max-time 2 \
  -X POST -H 'Content-Type: application/json' \
  -d "$PAYLOAD" "$BROKER_URL/notify" 2>/dev/null || true

exit 0
