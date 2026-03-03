#!/bin/bash
# cred-sync.sh — Container-side credential sync daemon.
# Communicates with the host-side credential broker over TCP.
# Pushes refreshed tokens up (on file change) and pulls newer tokens down.
#
# Environment:
#   MITTENS_BROKER_PORT — TCP port of the host broker
#
# Dependencies: curl, jq, md5sum (all present in the base image).
set -euo pipefail
trap 'exit 0' TERM

CRED_FILE="/home/claude/.claude/.credentials.json"
BROKER_PORT="${MITTENS_BROKER_PORT:-}"
POLL_INTERVAL=5
CURL_TIMEOUT=3

if [[ -z "$BROKER_PORT" ]]; then
    exit 0
fi

BROKER_URL="http://host.docker.internal:$BROKER_PORT"

# Log file inside the container.
LOGFILE="/tmp/cred-sync.log"

clog() {
    local ts
    ts=$(date '+%H:%M:%S.%3N' 2>/dev/null || date '+%H:%M:%S')
    echo "$ts [cred-sync] $*" >> "$LOGFILE" 2>/dev/null || true
}

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
        # Check root .expiresAt first, then nested (e.g. .claudeAiOauth.expiresAt).
        jq -r '[.expiresAt // 0, (.[] | objects | .expiresAt // 0)] | max' "$file" 2>/dev/null || echo "0"
    else
        echo "0"
    fi
}

broker_get() {
    curl --silent --fail --max-time "$CURL_TIMEOUT" --noproxy '*' \
        "$BROKER_URL/" 2>/dev/null || true
}

broker_put() {
    local data="$1"
    local http_code
    http_code=$(echo "$data" | curl --silent --output /dev/null --write-out '%{http_code}' \
        --max-time "$CURL_TIMEOUT" --noproxy '*' \
        -X PUT -H 'Content-Type: application/json' \
        --data-binary @- \
        "$BROKER_URL/" 2>/dev/null) || true
    echo "$http_code"
}

clog "started (broker: $BROKER_URL)"

# Connectivity check — log whether we can reach the broker at all.
if curl --silent --max-time 2 --noproxy '*' -o /dev/null "$BROKER_URL/" 2>/dev/null; then
    clog "broker reachable"
else
    clog "WARNING: broker NOT reachable at $BROKER_URL"
fi

# Initial push: seed the broker with our current credentials.
last_hash=$(compute_hash)
if [[ -f "$CRED_FILE" && -n "$last_hash" ]]; then
    data=$(cat "$CRED_FILE" 2>/dev/null) || true
    if [[ -n "$data" ]]; then
        local_exp=$(get_expires_at "$CRED_FILE")
        code=$(broker_put "$data")
        clog "initial push: expiresAt=$local_exp → $code"
    fi
else
    clog "no credentials file at startup"
fi

while true; do
    sleep "$POLL_INTERVAL" &
    wait $! 2>/dev/null || exit 0

    # --- Push: detect local file changes ---
    current_hash=$(compute_hash)
    if [[ -n "$current_hash" && "$current_hash" != "$last_hash" ]]; then
        data=$(cat "$CRED_FILE" 2>/dev/null) || true
        if [[ -n "$data" ]]; then
            local_exp=$(get_expires_at "$CRED_FILE")
            code=$(broker_put "$data")
            clog "push: file changed, expiresAt=$local_exp → $code"
            if [[ "$code" == "204" || "$code" == "409" || "$code" == "400" ]]; then
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

    remote_exp=$(echo "$remote" | jq -r '[.expiresAt // 0, (.[] | objects | .expiresAt // 0)] | max' 2>/dev/null) || continue
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
        clog "pull: updated local creds (remote: $remote_exp, was: $local_exp)"
    fi
done
