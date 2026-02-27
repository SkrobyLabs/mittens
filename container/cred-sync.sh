#!/bin/bash
# cred-sync.sh — Container-side credential sync daemon.
# Communicates with the host-side credential broker over a Unix socket.
# Pushes refreshed tokens up (on file change) and pulls newer tokens down.
#
# Environment:
#   MITTENS_CRED_BROKER_SOCK — path to the broker Unix socket
#
# Dependencies: curl, jq, md5sum (all present in the base image).
set -euo pipefail
trap 'exit 0' TERM

CRED_FILE="/home/claude/.claude/.credentials.json"
BROKER_SOCK="${MITTENS_CRED_BROKER_SOCK:-}"
POLL_INTERVAL=5
CURL_TIMEOUT=3

if [[ -z "$BROKER_SOCK" ]]; then
    exit 0
fi

# Track the md5 of the local file to detect changes.
last_hash=""

compute_hash() {
    if [[ -f "$CRED_FILE" ]]; then
        md5sum "$CRED_FILE" 2>/dev/null | cut -d' ' -f1
    else
        echo ""
    fi
}

get_expires_at() {
    local file="$1"
    if [[ -f "$file" ]]; then
        jq -r '.expiresAt // 0' "$file" 2>/dev/null || echo "0"
    else
        echo "0"
    fi
}

broker_get() {
    curl --silent --fail --unix-socket "$BROKER_SOCK" \
        --max-time "$CURL_TIMEOUT" \
        "http://broker/" 2>/dev/null || true
}

broker_put() {
    local data="$1"
    local http_code
    http_code=$(echo "$data" | curl --silent --output /dev/null --write-out '%{http_code}' \
        --unix-socket "$BROKER_SOCK" \
        --max-time "$CURL_TIMEOUT" \
        -X PUT -H 'Content-Type: application/json' \
        --data-binary @- \
        "http://broker/" 2>/dev/null) || true
    echo "$http_code"
}

# Initial push: seed the broker with our current credentials.
last_hash=$(compute_hash)
if [[ -f "$CRED_FILE" && -n "$last_hash" ]]; then
    data=$(cat "$CRED_FILE" 2>/dev/null) || true
    if [[ -n "$data" ]]; then
        broker_put "$data" >/dev/null
    fi
fi

while true; do
    sleep "$POLL_INTERVAL" &
    wait $! 2>/dev/null || exit 0

    # --- Push: detect local file changes ---
    current_hash=$(compute_hash)
    if [[ -n "$current_hash" && "$current_hash" != "$last_hash" ]]; then
        data=$(cat "$CRED_FILE" 2>/dev/null) || true
        if [[ -n "$data" ]]; then
            code=$(broker_put "$data")
            if [[ "$code" == "204" || "$code" == "409" ]]; then
                # 204 = accepted, 409 = broker already has fresher creds.
                # Either way, no need to re-push the same content.
                last_hash="$current_hash"
            fi
        fi
    fi

    # --- Pull: check if broker has newer credentials ---
    remote=$(broker_get)
    if [[ -z "$remote" ]]; then
        continue
    fi

    remote_exp=$(echo "$remote" | jq -r '.expiresAt // 0' 2>/dev/null) || continue
    local_exp=$(get_expires_at "$CRED_FILE")

    # Guard: both values must be integers for -gt comparison.
    [[ "$remote_exp" =~ ^[0-9]+$ ]] || continue
    [[ "$local_exp" =~ ^[0-9]+$ ]]  || continue

    if [[ "$remote_exp" -gt "$local_exp" ]]; then
        # Atomic write: tmp + mv.
        tmp="${CRED_FILE}.tmp.$$"
        echo "$remote" > "$tmp" && chmod 600 "$tmp" && mv "$tmp" "$CRED_FILE"
        # Update hash to prevent re-pushing what we just pulled.
        last_hash=$(compute_hash)
    fi
done
