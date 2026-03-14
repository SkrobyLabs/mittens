#!/bin/bash
# cred-sync.sh — Container-side credential sync daemon.
# Communicates with the host-side credential broker over TCP or Unix socket.
# Pushes refreshed tokens up (on file change) and pulls newer tokens down.
#
# Environment:
#   MITTENS_BROKER_PORT  — TCP port of the host broker (macOS/Windows)
#   MITTENS_BROKER_SOCK  — Unix socket path (Linux)
#   MITTENS_BROKER_TOKEN — optional auth token for broker access
#
# Dependencies: curl, jq, md5sum (all present in the base image).
set -euo pipefail
trap 'exit 0' TERM

AI_USERNAME="${AI_USERNAME:-claude}"
AI_CONFIG_DIR="${MITTENS_AI_CONFIG_DIR:-.claude}"
AI_CRED_FILE="${MITTENS_AI_CRED_FILE:-.credentials.json}"
CRED_FILE="/home/${AI_USERNAME}/${AI_CONFIG_DIR}/${AI_CRED_FILE}"
BROKER_PORT="${MITTENS_BROKER_PORT:-}"
BROKER_SOCK="${MITTENS_BROKER_SOCK:-}"
BROKER_TOKEN="${MITTENS_BROKER_TOKEN:-}"
POLL_INTERVAL=5
CURL_TIMEOUT=3
REFRESH_THRESHOLD_MS=300000  # trigger proactive refresh when <5 min remain

if [[ -z "$BROKER_PORT" && -z "$BROKER_SOCK" ]]; then
    exit 0
fi

# Build curl args for broker access (TCP vs Unix socket).
if [[ -n "$BROKER_SOCK" ]]; then
    BROKER_URL="http://broker"
    CURL_BROKER=(curl --unix-socket "$BROKER_SOCK")
else
    BROKER_URL="http://host.docker.internal:$BROKER_PORT"
    CURL_BROKER=(curl --noproxy '*')
fi

# broker_curl wraps CURL_BROKER with optional token auth header.
broker_curl() {
    if [[ -n "$BROKER_TOKEN" ]]; then
        "${CURL_BROKER[@]}" -H "X-Mittens-Token: $BROKER_TOKEN" "$@"
    else
        "${CURL_BROKER[@]}" "$@"
    fi
}

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
        # Check expiresAt (Claude), expires_at (Codex), expiry_date (Gemini) at root and nested levels.
        jq -r '[.expiresAt // 0, .expires_at // 0, .expiry_date // 0, (.[] | objects | (.expiresAt // 0, .expires_at // 0))] | max' "$file" 2>/dev/null || echo "0"
    else
        echo "0"
    fi
}

broker_get() {
    broker_curl --silent --fail --max-time "$CURL_TIMEOUT" \
        "$BROKER_URL/" 2>/dev/null || true
}

broker_put() {
    local data="$1"
    local http_code
    http_code=$(echo "$data" | broker_curl --silent --output /dev/null --write-out '%{http_code}' \
        --max-time "$CURL_TIMEOUT" \
        -X PUT -H 'Content-Type: application/json' \
        --data-binary @- \
        "$BROKER_URL/" 2>/dev/null) || true
    echo "$http_code"
}

# Ask the broker whether this container should perform a proactive refresh.
# Returns "refresh" (this container is the coordinator) or "wait" (another is).
broker_refresh_request() {
    local result
    result=$(broker_curl --silent --max-time "$CURL_TIMEOUT" \
        -X POST "$BROKER_URL/refresh" 2>/dev/null) || echo '{"action":"wait"}'
    echo "$result" | jq -r '.action // "wait"' 2>/dev/null || echo "wait"
}

# Trigger proactive token refresh by faking an early expiry in the local
# credential file. Claude Code checks expiresAt before API calls and will
# use its internal refreshToken flow when it sees the token as expired.
# NOTE: Gemini credentials use expiry_date and are managed by Gemini's own
# OAuth2Client — skip proactive refresh for that format.
trigger_token_refresh() {
    # Detect Gemini format: has expiry_date but not claudeAiOauth/expiresAt/expires_at.
    local is_gemini
    is_gemini=$(jq -r 'if has("expiry_date") and (has("claudeAiOauth") | not) and (has("expiresAt") | not) and (has("expires_at") | not) then "yes" else "no" end' "$CRED_FILE" 2>/dev/null) || is_gemini="no"
    if [[ "$is_gemini" == "yes" ]]; then
        clog "proactive refresh: skipping (Gemini manages its own OAuth refresh)"
        return
    fi
    local tmp="${CRED_FILE}.refresh.$$"
    jq 'if has("claudeAiOauth") then .claudeAiOauth.expiresAt = 1
         elif has("expires_at") then .expires_at = 1
         else .expiresAt = 1 end' \
        "$CRED_FILE" > "$tmp" 2>/dev/null \
        && chmod 600 "$tmp" && mv "$tmp" "$CRED_FILE" \
        || { rm -f "$tmp"; clog "proactive refresh: failed to rewrite expiresAt"; return; }
    # Update hash so the push phase doesn't send the faked-expiry value to the
    # broker (which would corrupt credentials for other containers).
    last_hash=$(compute_hash)
    clog "proactive refresh: set early expiry, waiting for Claude Code to refresh"
}

clog "started (broker: $BROKER_URL)"

# Connectivity check — log whether we can reach the broker at all.
if broker_curl --silent --max-time 2 -o /dev/null "$BROKER_URL/" 2>/dev/null; then
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
    if [[ -n "$remote" ]]; then
        remote_exp=$(echo "$remote" | jq -r '[.expiresAt // 0, .expires_at // 0, .expiry_date // 0, (.[] | objects | (.expiresAt // 0, .expires_at // 0))] | max' 2>/dev/null) || true
        local_exp=$(get_expires_at "$CRED_FILE")

        if [[ "$remote_exp" =~ ^[0-9]+$ && "$local_exp" =~ ^[0-9]+$ ]]; then
            if [[ "$remote_exp" -gt "$local_exp" ]]; then
                # Atomic write: tmp + mv.
                tmp="${CRED_FILE}.tmp.$$"
                echo "$remote" > "$tmp" && chmod 600 "$tmp" && mv "$tmp" "$CRED_FILE"
                # Update hash to prevent re-pushing what we just pulled.
                last_hash=$(compute_hash)
                clog "pull: updated local creds (remote: $remote_exp, was: $local_exp)"
            fi
        fi
    fi

    # --- Proactive refresh: trigger before credentials expire ---
    cur_exp=$(get_expires_at "$CRED_FILE")
    now_ms=$(date +%s%3N 2>/dev/null || echo "$(( $(date +%s) * 1000 ))")
    if [[ "$cur_exp" =~ ^[0-9]+$ && "$now_ms" =~ ^[0-9]+$ ]]; then
        remaining=$(( cur_exp - now_ms ))
        if [[ "$remaining" -gt 0 && "$remaining" -lt "$REFRESH_THRESHOLD_MS" ]]; then
            action=$(broker_refresh_request)
            if [[ "$action" == "refresh" ]]; then
                clog "proactive refresh: triggering (expires in ${remaining}ms)"
                trigger_token_refresh
            else
                clog "proactive refresh: another container is handling it"
            fi
        fi
    fi
done
