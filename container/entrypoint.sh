#!/bin/bash
set -e

CONFIG_MOUNT="/mnt/claude-config"
AI_USERNAME="${AI_USERNAME:-claude}"
AI_HOME="/home/${AI_USERNAME}"
AI_CONFIG_DIR="${MITTENS_AI_CONFIG_DIR:-.claude}"
AI_DIR="${AI_HOME}/${AI_CONFIG_DIR}"
AI_CRED_FILE="${MITTENS_AI_CRED_FILE:-.credentials.json}"
AI_PREFS_FILE="${MITTENS_AI_PREFS_FILE:-.claude.json}"
AI_SETTINGS_FILE="${MITTENS_AI_SETTINGS_FILE:-settings.json}"
AI_PROJECT_FILE="${MITTENS_AI_PROJECT_FILE:-CLAUDE.md}"
AI_TRUSTED_DIRS_KEY="${MITTENS_AI_TRUSTED_DIRS_KEY:-}"
AI_YOLO_KEY="${MITTENS_AI_YOLO_KEY:-}"
AI_MCP_SERVERS_KEY="${MITTENS_AI_MCP_SERVERS_KEY:-mcpServers}"
AI_TRUSTED_DIRS_FILE="${MITTENS_AI_TRUSTED_DIRS_FILE:-}"
AI_INIT_SETTINGS_JQ="${MITTENS_AI_INIT_SETTINGS_JQ:-}"
AI_STOP_HOOK_EVENT="${MITTENS_AI_STOP_HOOK_EVENT:-}"
AI_PERSIST_FILES="${MITTENS_AI_PERSIST_FILES:-}"
AI_SETTINGS_FORMAT="${MITTENS_AI_SETTINGS_FORMAT:-json}"
AI_CONFIG_SUBDIRS="${MITTENS_AI_CONFIG_SUBDIRS:-skills,hooks,agents,output-styles}"
AI_PLUGIN_DIR="${MITTENS_AI_PLUGIN_DIR:-plugins}"
AI_PLUGIN_FILES="${MITTENS_AI_PLUGIN_FILES:-installed_plugins.json,known_marketplaces.json,config.json}"
FIREWALL_CONF="/mnt/claude-config/firewall.conf"

# ===========================================================
# Phase 1: Root-level setup (runs as root inside container)
# ===========================================================
if [[ "$(id -u)" == "0" ]]; then

    # ── Docker-in-Docker ──────────────────────────────────
    if [[ "${MITTENS_DIND:-false}" == "true" ]]; then
        echo "[mittens] Starting Docker daemon..."

        dockerd \
            --host=unix:///var/run/docker.sock \
            --storage-driver=overlay2 \
            > /tmp/dockerd.log 2>&1 &

        retries=30
        while ! docker info &>/dev/null 2>&1 && [[ $retries -gt 0 ]]; do
            retries=$((retries - 1))
            sleep 1
        done

        if docker info &>/dev/null 2>&1; then
            echo "[mittens] Docker daemon ready"
        else
            echo "[mittens] Warning: Docker daemon failed to start" >&2
            tail -20 /tmp/dockerd.log >&2
        fi
    fi

    # ── Host Docker socket ────────────────────────────────
    if [[ "${MITTENS_DOCKER_HOST:-false}" == "true" ]]; then
        echo "[mittens] Using host Docker socket"
        SOCK="/var/run/docker.sock"
        if [[ -S "$SOCK" ]]; then
            chmod 666 "$SOCK"
        fi
        if docker info &>/dev/null 2>&1; then
            echo "[mittens] Host Docker daemon accessible"
        else
            echo "[mittens] Warning: host Docker socket not accessible" >&2
        fi
    fi

    # ── Network firewall (Squid proxy + iptables) ────────
    if [[ "${MITTENS_FIREWALL:-false}" == "true" && -f "$FIREWALL_CONF" ]]; then
        echo "[mittens] Applying network firewall..."

        # Generate Squid domain whitelist from firewall.conf
        sed 's/#.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' "$FIREWALL_CONF" \
            | grep -v '^$' > /etc/squid/whitelist.txt

        # ── MCP server domain passthrough ──────────────────
        if [[ -n "${MITTENS_MCP:-}" ]]; then
            MCP_DOMAINS_BUILTIN="/etc/mittens/mcp-domains.conf"
            MCP_DOMAINS_USER="/mnt/claude-config/${AI_CONFIG_DIR}/mcp-domains.conf"
            CLAUDE_JSON="/mnt/claude-config/${AI_PREFS_FILE}"
            mcp_count=0

            # Load domain mappings (user overrides built-in)
            declare -A MCP_MAP
            for mapfile in "$MCP_DOMAINS_BUILTIN" "$MCP_DOMAINS_USER"; do
                if [[ -f "$mapfile" ]]; then
                    while IFS='=' read -r name domains; do
                        [[ -z "$name" || "$name" == \#* ]] && continue
                        name=$(echo "$name" | tr -d '[:space:]')
                        domains=$(echo "$domains" | tr -d '[:space:]')
                        [[ -n "$name" && -n "$domains" ]] && MCP_MAP["$name"]="$domains"
                    done < "$mapfile"
                fi
            done

            # Determine which servers to resolve
            if [[ "$MITTENS_MCP" == "__all__" ]]; then
                # Collect all MCP server names from .claude.json
                mcp_list=""
                if [[ -f "$CLAUDE_JSON" ]]; then
                    mcp_list=$(jq -r --arg key "$AI_MCP_SERVERS_KEY" '.[$key] // {} | keys[]' "$CLAUDE_JSON" 2>/dev/null || true)
                fi
                # Also check project-level .mcp.json
                if [[ -f /workspace/.mcp.json ]]; then
                    project_mcp=$(jq -r '.mcpServers // {} | keys[]' /workspace/.mcp.json 2>/dev/null || true)
                    mcp_list=$(printf '%s\n%s' "$mcp_list" "$project_mcp")
                fi
            else
                # Comma-separated list → newline-separated
                mcp_list=$(echo "$MITTENS_MCP" | tr ',' '\n')
            fi

            # Resolve each server to domains
            while IFS= read -r server; do
                [[ -z "$server" ]] && continue
                server=$(echo "$server" | tr -d '[:space:]')
                resolved=""

                # Check if server is SSE/HTTP type with a URL in config
                if [[ -f "$CLAUDE_JSON" ]]; then
                    url=$(jq -r --arg s "$server" --arg key "$AI_MCP_SERVERS_KEY" '.[$key][$s].url // empty' "$CLAUDE_JSON" 2>/dev/null || true)
                    if [[ -n "$url" ]]; then
                        # Extract hostname from URL
                        resolved=$(echo "$url" | sed -E 's|^https?://([^/:]+).*|\1|')
                    fi
                fi

                # Fall back to lookup table
                if [[ -z "$resolved" && -n "${MCP_MAP[$server]+x}" ]]; then
                    resolved="${MCP_MAP[$server]}"
                fi

                if [[ -n "$resolved" ]]; then
                    # resolved may be comma-separated; split and append
                    echo "$resolved" | tr ',' '\n' >> /etc/squid/whitelist.txt
                    mcp_count=$((mcp_count + $(echo "$resolved" | tr ',' '\n' | wc -l)))
                else
                    echo "[mittens] Warning: no domain mapping for MCP server '$server'" >&2
                fi
            done <<< "$mcp_list"

            if [[ $mcp_count -gt 0 ]]; then
                echo "[mittens] MCP passthrough: added $mcp_count domain(s) to whitelist"
            fi
        fi

        # Extension-declared extra domains (comma-separated env var)
        if [[ -n "${MITTENS_FIREWALL_EXTRA:-}" ]]; then
            echo "$MITTENS_FIREWALL_EXTRA" | tr ',' '\n' >> /etc/squid/whitelist.txt
        fi

        # Deduplicate whitelist
        sort -u /etc/squid/whitelist.txt -o /etc/squid/whitelist.txt

        domain_count=$(wc -l < /etc/squid/whitelist.txt | tr -d ' ')

        # Start Squid (FQDN-based HTTP/HTTPS filtering)
        squid -f /etc/squid/squid.conf
        retries=40
        while ! (echo > /dev/tcp/127.0.0.1/3128) 2>/dev/null && [[ $retries -gt 0 ]]; do
            retries=$((retries - 1))
            sleep 0.25
        done

        if (echo > /dev/tcp/127.0.0.1/3128) 2>/dev/null; then
            echo "[mittens] Proxy ready ($domain_count domains whitelisted)"
        else
            echo "[mittens] Warning: Squid proxy failed to start" >&2
            cat /var/log/squid/cache.log >&2 2>/dev/null || true
        fi

        # iptables: force all HTTP(S) through the proxy
        # Only the squid process (proxy user) can make direct outbound connections.
        # Everything else must go through the proxy on localhost:3128.
        for cmd in iptables ip6tables; do
            $cmd -F OUTPUT 2>/dev/null || true
            $cmd -P OUTPUT DROP
            $cmd -A OUTPUT -o lo -j ACCEPT
            $cmd -A OUTPUT -m state --state ESTABLISHED,RELATED -j ACCEPT
            # DNS (so apps can resolve — Squid also needs this)
            $cmd -A OUTPUT -p udp --dport 53 -j ACCEPT
            $cmd -A OUTPUT -p tcp --dport 53 -j ACCEPT
            # Only squid (proxy user) may connect to the internet on 80/443
            $cmd -A OUTPUT -m owner --uid-owner proxy -p tcp --dport 443 -j ACCEPT
            $cmd -A OUTPUT -m owner --uid-owner proxy -p tcp --dport 80 -j ACCEPT
            # SSH for git (not proxied — allowed directly)
            $cmd -A OUTPUT -p tcp --dport 22 -j ACCEPT
        done

        # Allow container to reach the host broker (credential sync + URL opening).
        # Use port-only matching — the broker port is random and ephemeral, so
        # restricting by destination IP is fragile (IPv4/IPv6 mismatch on macOS)
        # and the minimal port-based rule is sufficient.
        if [[ -n "${MITTENS_BROKER_PORT:-}" ]]; then
            for cmd in iptables ip6tables; do
                $cmd -A OUTPUT -p tcp --dport "$MITTENS_BROKER_PORT" -j ACCEPT 2>/dev/null || true
            done
        fi

        # Set proxy env vars — inherited by claude user via gosu
        export HTTP_PROXY=http://127.0.0.1:3128
        export HTTPS_PROXY=http://127.0.0.1:3128
        export http_proxy=http://127.0.0.1:3128
        export https_proxy=http://127.0.0.1:3128
        export NO_PROXY=localhost,127.0.0.1,::1,host.docker.internal
        export no_proxy=localhost,127.0.0.1,::1,host.docker.internal

        # Node 22+ native fetch (undici) ignores HTTP_PROXY by default.
        # --use-env-proxy makes it honour the proxy env vars above.
        export NODE_OPTIONS="${NODE_OPTIONS:+$NODE_OPTIONS }--use-env-proxy"

        echo "[mittens] Firewall active: outbound HTTP(S) restricted to whitelisted domains"
    fi

    # Drop privileges and re-exec this script as the AI user
    exec gosu "$AI_USERNAME" "$0" "$@"
fi

# ===========================================================
# Phase 2: User-level setup (runs as AI user)
# ===========================================================

mkdir -p "$AI_DIR"

# --- Source extension environment (Go, .NET, etc.) ---
for f in /etc/profile.d/*.sh; do
    [ -r "$f" ] && . "$f"
done

# --- Ensure ~/.local/bin/<binary> exists so AI CLI diagnostics are clean ---
AI_BINARY="${MITTENS_AI_BINARY:-claude}"
mkdir -p "$AI_HOME/.local/bin"
if command -v "$AI_BINARY" &>/dev/null && [[ ! -e "$AI_HOME/.local/bin/$AI_BINARY" ]]; then
    ln -s "$(command -v "$AI_BINARY")" "$AI_HOME/.local/bin/$AI_BINARY"
fi
export PATH="$AI_HOME/.local/bin:$PATH"

# --- Copy read-only config into writable home ---
STAGING_CONFIG="${CONFIG_MOUNT}/${AI_CONFIG_DIR}"
if [[ -d "$STAGING_CONFIG" ]]; then
    # Config subdirectories (provider-defined, e.g. skills,hooks,agents,output-styles)
    if [[ -n "$AI_CONFIG_SUBDIRS" ]]; then
        IFS=',' read -ra _subdirs <<< "$AI_CONFIG_SUBDIRS"
        for item in "${_subdirs[@]}"; do
            if [[ -d "$STAGING_CONFIG/$item" ]]; then
                cp -a "$STAGING_CONFIG/$item" "$AI_DIR/$item"
            fi
        done
    fi

    # Plugin config files (provider-defined, not cache)
    if [[ -n "$AI_PLUGIN_DIR" && -d "$STAGING_CONFIG/$AI_PLUGIN_DIR" ]]; then
        mkdir -p "$AI_DIR/$AI_PLUGIN_DIR"
        if [[ -n "$AI_PLUGIN_FILES" ]]; then
            IFS=',' read -ra _pfiles <<< "$AI_PLUGIN_FILES"
            for file in "${_pfiles[@]}"; do
                if [[ -f "$STAGING_CONFIG/$AI_PLUGIN_DIR/$file" ]]; then
                    cp "$STAGING_CONFIG/$AI_PLUGIN_DIR/$file" "$AI_DIR/$AI_PLUGIN_DIR/$file"
                fi
            done
        fi
        if [[ -d "$STAGING_CONFIG/$AI_PLUGIN_DIR/marketplaces" ]]; then
            cp -a "$STAGING_CONFIG/$AI_PLUGIN_DIR/marketplaces" "$AI_DIR/$AI_PLUGIN_DIR/marketplaces"
        fi
    fi

    # Config files
    for file in ${AI_SETTINGS_FILE} settings.local.json ${AI_PROJECT_FILE} statusline.sh; do
        if [[ -f "$STAGING_CONFIG/$file" ]]; then
            cp "$STAGING_CONFIG/$file" "$AI_DIR/$file"
        fi
    done

    # Make statusline executable if copied
    [[ -f "$AI_DIR/statusline.sh" ]] && chmod +x "$AI_DIR/statusline.sh"

    # Persist files: state files that survive between runs (e.g. google_accounts.json, installation_id)
    if [[ -n "$AI_PERSIST_FILES" ]]; then
        IFS=',' read -ra _persist <<< "$AI_PERSIST_FILES"
        for file in "${_persist[@]}"; do
            if [[ -f "$STAGING_CONFIG/$file" ]]; then
                cp "$STAGING_CONFIG/$file" "$AI_DIR/$file"
            fi
        done
    fi
fi

# --- Pre-trust /workspace (and host workspace path + extra dirs if set) ---
SETTINGS_FILE="$AI_DIR/${AI_SETTINGS_FILE}"
if [[ -n "$AI_TRUSTED_DIRS_KEY" && "$AI_SETTINGS_FORMAT" == "json" ]]; then
    TRUST_DIRS='["/workspace"]'
    if [[ -n "${MITTENS_HOST_WORKSPACE:-}" && "$MITTENS_HOST_WORKSPACE" != "/workspace" ]]; then
        TRUST_DIRS=$(echo "$TRUST_DIRS" | jq --arg d "$MITTENS_HOST_WORKSPACE" '. + [$d]')
    fi
    if [[ -n "${MITTENS_EXTRA_DIRS:-}" ]]; then
        TRUST_DIRS=$(echo "$TRUST_DIRS" | jq --arg dirs "$MITTENS_EXTRA_DIRS" '. + ($dirs | split(":"))')
    fi
    if [[ -f "$SETTINGS_FILE" ]]; then
        jq --argjson dirs "$TRUST_DIRS" --arg key "$AI_TRUSTED_DIRS_KEY" \
            '.[$key] = ((.[$key] // []) + $dirs | unique)' \
            "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
    else
        jq -n --argjson dirs "$TRUST_DIRS" --arg key "$AI_TRUSTED_DIRS_KEY" '{($key): $dirs}' > "$SETTINGS_FILE"
    fi
fi

# --- Write trusted dirs file (providers that use a separate file, e.g. Gemini) ---
if [[ -n "$AI_TRUSTED_DIRS_FILE" ]]; then
    TRUST_OBJ='{"/workspace": "TRUST_FOLDER"}'
    if [[ -n "${MITTENS_HOST_WORKSPACE:-}" && "$MITTENS_HOST_WORKSPACE" != "/workspace" ]]; then
        TRUST_OBJ=$(echo "$TRUST_OBJ" | jq --arg d "$MITTENS_HOST_WORKSPACE" '. + {($d): "TRUST_FOLDER"}')
    fi
    if [[ -n "${MITTENS_EXTRA_DIRS:-}" ]]; then
        while IFS= read -r _ed; do
            [[ -n "$_ed" ]] && TRUST_OBJ=$(echo "$TRUST_OBJ" | jq --arg d "$_ed" '. + {($d): "TRUST_FOLDER"}')
        done < <(echo "$MITTENS_EXTRA_DIRS" | tr ':' '\n')
    fi
    echo "$TRUST_OBJ" > "$AI_DIR/${AI_TRUSTED_DIRS_FILE}"
fi

# --- Auto-accept yolo permission prompt ---
if [[ "${MITTENS_YOLO:-false}" == "true" && -n "$AI_YOLO_KEY" && "$AI_SETTINGS_FORMAT" == "json" ]]; then
    jq --arg key "$AI_YOLO_KEY" '.[$key] = true' "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
fi

# --- Provider-specific settings init (e.g. disable auto-update) ---
if [[ -n "$AI_INIT_SETTINGS_JQ" && "$AI_SETTINGS_FORMAT" == "json" ]]; then
    [[ -f "$SETTINGS_FILE" ]] || echo '{}' > "$SETTINGS_FILE"
    jq "$AI_INIT_SETTINGS_JQ" "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
fi

# --- Copy git config and mark mounted paths as safe ---
if [[ -f "$CONFIG_MOUNT/.gitconfig" ]]; then
    cp "$CONFIG_MOUNT/.gitconfig" "$AI_HOME/.gitconfig"
fi
git config --global --add safe.directory '*'

# --- Copy user preferences ---
if [[ -n "$AI_PREFS_FILE" && -f "$CONFIG_MOUNT/${AI_PREFS_FILE}" ]]; then
    cp "$CONFIG_MOUNT/${AI_PREFS_FILE}" "$AI_HOME/${AI_PREFS_FILE}"
fi

# --- Write OAuth credentials into the plaintext credential store ---
if [[ -f "$CONFIG_MOUNT/${AI_CRED_FILE}" ]]; then
    cp "$CONFIG_MOUNT/${AI_CRED_FILE}" "$AI_DIR/${AI_CRED_FILE}"
    chmod 600 "$AI_DIR/${AI_CRED_FILE}"
fi

# --- Inform AI about extra workspace directories ---
if [[ -n "${MITTENS_EXTRA_DIRS:-}" ]]; then
    {
        echo ""
        echo "# Additional Workspace Directories"
        echo "These directories are mounted read-write and trusted. You can read, edit, and search files in them."
        while IFS= read -r _ed; do
            [[ -n "$_ed" ]] && echo "- ${_ed}"
        done < <(echo "$MITTENS_EXTRA_DIRS" | tr ':' '\n')
    } >> "$AI_DIR/${AI_PROJECT_FILE}"
fi

# --- Inform AI about network firewall restrictions ---
if [[ "${MITTENS_FIREWALL:-false}" == "true" && -f /etc/squid/whitelist.txt ]]; then
    {
        echo ""
        echo "# Network Firewall"
        echo "This container runs behind an outbound network firewall (Squid proxy + iptables)."
        echo "Only the domains listed below are reachable over HTTP/HTTPS."
        echo "Requests to any other FQDN will **time out or be refused** by the proxy — do not retry, the domain is blocked by policy."
        echo ""
        echo "If a tool or package manager fails with a network error, check whether the target domain is in this list before troubleshooting further."
        echo ""
        echo "## Whitelisted domains"
        echo '```'
        cat /etc/squid/whitelist.txt
        echo '```'
    } >> "$AI_DIR/${AI_PROJECT_FILE}"
fi

# --- Inject notification hooks (if broker port is available, JSON settings only) ---
if [[ -n "${MITTENS_BROKER_PORT:-}" && -z "${MITTENS_NO_NOTIFY:-}" && "$AI_SETTINGS_FORMAT" == "json" ]]; then
    [[ -f "$SETTINGS_FILE" ]] || echo '{}' > "$SETTINGS_FILE"
    NOTIFY_CMD="MSG=\$(jq -r '.message // \"needs attention\"'); /usr/local/bin/notify.sh notification \"\$MSG\""
    HOOKS_JSON=$(jq -n \
        --arg stop_event "$AI_STOP_HOOK_EVENT" \
        --arg notify_cmd "$NOTIFY_CMD" \
        '{hooks: {
            "Notification": [{"hooks": [{"type": "command", "command": $notify_cmd}]}]
        }} | if $stop_event != "" then .hooks[$stop_event] = [{"hooks": [{"type": "command", "command": "/usr/local/bin/notify.sh stop"}]}] else . end')
    jq -s '.[0] * .[1]' "$SETTINGS_FILE" <(echo "$HOOKS_JSON") > "${SETTINGS_FILE}.tmp" \
        && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
fi

# --- Start credential sync daemon (if broker port is available) ---
if [[ -n "${MITTENS_BROKER_PORT:-}" ]]; then
    /usr/local/bin/cred-sync.sh &
fi

# --- cd to host workspace path so Claude computes the correct project dir ---
if [[ -n "${MITTENS_HOST_WORKSPACE:-}" && "$MITTENS_HOST_WORKSPACE" != "/workspace" ]]; then
    cd "$MITTENS_HOST_WORKSPACE"
fi

exec "$@"
