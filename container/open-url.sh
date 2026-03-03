#!/bin/bash
# xdg-open shim — forwards URLs to the host via the mittens broker socket.
# Inside the container there is no desktop environment, so this shim
# ensures that browser-open requests (e.g. /login in Claude Code) reach
# the host and open in the real browser.

URL="$1"
[[ -z "$URL" ]] && exit 1

SOCK="${MITTENS_CRED_BROKER_SOCK:-/tmp/mittens-broker/broker.sock}"
if [[ -S "$SOCK" ]]; then
    curl -sf --unix-socket "$SOCK" -X POST -d "$URL" http://localhost/open 2>/dev/null || true
fi
