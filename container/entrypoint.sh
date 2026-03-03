#!/bin/bash
set -e

CONFIG_MOUNT="/mnt/claude-config"
CLAUDE_HOME="/home/claude"
CLAUDE_DIR="${CLAUDE_HOME}/.claude"
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

    # ── Network firewall (Squid proxy + iptables) ────────
    if [[ "${MITTENS_FIREWALL:-false}" == "true" && -f "$FIREWALL_CONF" ]]; then
        echo "[mittens] Applying network firewall..."

        # Generate Squid domain whitelist from firewall.conf
        sed 's/#.*//; s/^[[:space:]]*//; s/[[:space:]]*$//' "$FIREWALL_CONF" \
            | grep -v '^$' > /etc/squid/whitelist.txt

        # ── MCP server domain passthrough ──────────────────
        if [[ -n "${MITTENS_MCP:-}" ]]; then
            MCP_DOMAINS_BUILTIN="/etc/mittens/mcp-domains.conf"
            MCP_DOMAINS_USER="/mnt/claude-config/.claude/mcp-domains.conf"
            CLAUDE_JSON="/mnt/claude-config/.claude.json"
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
                    mcp_list=$(jq -r '.mcpServers // {} | keys[]' "$CLAUDE_JSON" 2>/dev/null || true)
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
                    url=$(jq -r --arg s "$server" '.mcpServers[$s].url // empty' "$CLAUDE_JSON" 2>/dev/null || true)
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
        retries=20
        while [[ ! -f /run/squid.pid ]] && [[ $retries -gt 0 ]]; do
            retries=$((retries - 1))
            sleep 0.25
        done

        if [[ -f /run/squid.pid ]]; then
            echo "[mittens] Proxy ready ($domain_count domains whitelisted)"
        else
            echo "[mittens] Warning: Squid proxy failed to start" >&2
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

        echo "[mittens] Firewall active: outbound HTTP(S) restricted to whitelisted domains"
    fi

    # Drop privileges and re-exec this script as the claude user
    exec gosu claude "$0" "$@"
fi

# ===========================================================
# Phase 2: User-level setup (runs as claude)
# ===========================================================

mkdir -p "$CLAUDE_DIR"

# --- Source extension environment (Go, .NET, etc.) ---
for f in /etc/profile.d/*.sh; do
    [ -r "$f" ] && . "$f"
done

# --- Ensure ~/.local/bin/claude exists so Claude Code diagnostics are clean ---
mkdir -p "$CLAUDE_HOME/.local/bin"
if command -v claude &>/dev/null && [[ ! -e "$CLAUDE_HOME/.local/bin/claude" ]]; then
    ln -s "$(command -v claude)" "$CLAUDE_HOME/.local/bin/claude"
fi
export PATH="$CLAUDE_HOME/.local/bin:$PATH"

# --- Copy read-only config into writable home ---
if [[ -d "$CONFIG_MOUNT/.claude" ]]; then
    # Config directories (skills, hooks, agents, plugins, etc.)
    for item in skills hooks agents output-styles; do
        if [[ -d "$CONFIG_MOUNT/.claude/$item" ]]; then
            cp -a "$CONFIG_MOUNT/.claude/$item" "$CLAUDE_DIR/$item"
        fi
    done

    # Plugin config files (not cache)
    if [[ -d "$CONFIG_MOUNT/.claude/plugins" ]]; then
        mkdir -p "$CLAUDE_DIR/plugins"
        for file in installed_plugins.json known_marketplaces.json config.json; do
            if [[ -f "$CONFIG_MOUNT/.claude/plugins/$file" ]]; then
                cp "$CONFIG_MOUNT/.claude/plugins/$file" "$CLAUDE_DIR/plugins/$file"
            fi
        done
        if [[ -d "$CONFIG_MOUNT/.claude/plugins/marketplaces" ]]; then
            cp -a "$CONFIG_MOUNT/.claude/plugins/marketplaces" "$CLAUDE_DIR/plugins/marketplaces"
        fi
    fi

    # Config files
    for file in settings.json settings.local.json CLAUDE.md statusline.sh; do
        if [[ -f "$CONFIG_MOUNT/.claude/$file" ]]; then
            cp "$CONFIG_MOUNT/.claude/$file" "$CLAUDE_DIR/$file"
        fi
    done

    # Make statusline executable if copied
    [[ -f "$CLAUDE_DIR/statusline.sh" ]] && chmod +x "$CLAUDE_DIR/statusline.sh"
fi

# --- Pre-trust /workspace (and host workspace path + extra dirs if set) ---
SETTINGS_FILE="$CLAUDE_DIR/settings.json"
TRUST_DIRS='["/workspace"]'
if [[ -n "${MITTENS_HOST_WORKSPACE:-}" && "$MITTENS_HOST_WORKSPACE" != "/workspace" ]]; then
    TRUST_DIRS=$(echo "$TRUST_DIRS" | jq --arg d "$MITTENS_HOST_WORKSPACE" '. + [$d]')
fi
if [[ -n "${MITTENS_EXTRA_DIRS:-}" ]]; then
    TRUST_DIRS=$(echo "$TRUST_DIRS" | jq --arg dirs "$MITTENS_EXTRA_DIRS" '. + ($dirs | split(":"))')
fi
if [[ -f "$SETTINGS_FILE" ]]; then
    jq --argjson dirs "$TRUST_DIRS" \
        '.trustedDirectories = ((.trustedDirectories // []) + $dirs | unique)' \
        "$SETTINGS_FILE" > "${SETTINGS_FILE}.tmp" && mv "${SETTINGS_FILE}.tmp" "$SETTINGS_FILE"
else
    jq -n --argjson dirs "$TRUST_DIRS" '{"trustedDirectories": $dirs}' > "$SETTINGS_FILE"
fi

# --- Copy git config and mark mounted paths as safe ---
if [[ -f "$CONFIG_MOUNT/.gitconfig" ]]; then
    cp "$CONFIG_MOUNT/.gitconfig" "$CLAUDE_HOME/.gitconfig"
fi
git config --global --add safe.directory '*'

# --- Copy user preferences (.claude.json) ---
if [[ -f "$CONFIG_MOUNT/.claude.json" ]]; then
    cp "$CONFIG_MOUNT/.claude.json" "$CLAUDE_HOME/.claude.json"
fi

# --- Write OAuth credentials into the plaintext credential store ---
if [[ -f "$CONFIG_MOUNT/.credentials.json" ]]; then
    cp "$CONFIG_MOUNT/.credentials.json" "$CLAUDE_DIR/.credentials.json"
    chmod 600 "$CLAUDE_DIR/.credentials.json"
fi

# --- Inform Claude about extra workspace directories ---
if [[ -n "${MITTENS_EXTRA_DIRS:-}" ]]; then
    {
        echo ""
        echo "# Additional Workspace Directories"
        echo "These directories are mounted read-write and trusted. You can read, edit, and search files in them."
        while IFS= read -r _ed; do
            [[ -n "$_ed" ]] && echo "- ${_ed}"
        done < <(echo "$MITTENS_EXTRA_DIRS" | tr ':' '\n')
    } >> "$CLAUDE_DIR/CLAUDE.md"
fi

# --- Inform Claude about network firewall restrictions ---
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
    } >> "$CLAUDE_DIR/CLAUDE.md"
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
