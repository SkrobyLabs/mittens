#!/usr/bin/env bash
# channel-client.sh — Send a JSON request to the mittens-ui channel socket
# and read the JSON response.
#
# Usage: channel-client.sh '{"id":"uuid","type":"add-dir","payload":{"path":"/foo"}}'
#
# Requires: MITTENS_CHANNEL_SOCK env var pointing to the Unix socket.
# Uses socat if available, falls back to bash /dev/tcp (not available for Unix sockets)
# or nc (netcat-openbsd) with -U flag.

set -euo pipefail

if [ -z "${MITTENS_CHANNEL_SOCK:-}" ]; then
  echo '{"error":"MITTENS_CHANNEL_SOCK not set"}' >&2
  exit 1
fi

if [ -z "${1:-}" ]; then
  echo '{"error":"usage: channel-client.sh JSON_MESSAGE"}' >&2
  exit 1
fi

SOCK="$MITTENS_CHANNEL_SOCK"
MSG="$1"

# Try socat first (most reliable for Unix sockets).
if command -v socat >/dev/null 2>&1; then
  echo "$MSG" | socat - "UNIX-CONNECT:$SOCK"
  exit $?
fi

# Try nc with -U (Unix socket support, netcat-openbsd).
if command -v nc >/dev/null 2>&1; then
  echo "$MSG" | nc -U "$SOCK"
  exit $?
fi

echo '{"error":"no socat or nc available"}' >&2
exit 1
