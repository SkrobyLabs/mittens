#!/bin/bash
# xdg-open shim — forwards URLs to the host via the mittens broker.
# Inside the container there is no desktop environment, so this shim
# ensures that browser-open requests (e.g. /login in Claude Code) reach
# the host and open in the real browser.
#
# For OAuth login flows, it also polls the broker for the intercepted
# callback and replays it to Claude Code's local callback server.

URL="$1"
[[ -z "$URL" ]] && exit 1

PORT="${MITTENS_BROKER_PORT:-}"
SOCK="${MITTENS_BROKER_SOCK:-}"

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

"${CURL_BROKER[@]}" -sf -X POST -d "$URL" "$BROKER_URL/open" 2>/dev/null || true

# If this is an OAuth URL with a localhost/127.0.0.1 callback, poll the broker
# for the intercepted callback and replay it to the AI CLI's local server.
if [[ "$URL" == *"redirect_uri="*"localhost"* || "$URL" == *"redirect_uri="*"127.0.0.1"* ]]; then
    (
        for _ in $(seq 1 120); do
            sleep 1
            callback=$("${CURL_BROKER[@]}" -sf "$BROKER_URL/login-callback" 2>/dev/null) || continue
            if [[ -n "$callback" ]]; then
                # Replay to Claude Code's callback server inside the container.
                curl -sf "$callback" >/dev/null 2>&1 || true
                break
            fi
        done
    ) &
fi
