# Mittens - Isolated Claude Code Environment
FROM node:22-bookworm

# Avoid prompts during package installation
ENV DEBIAN_FRONTEND=noninteractive

# Install common development tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    # Core utilities
    git \
    curl \
    jq \
    less \
    vim \
    # Build essentials
    build-essential \
    # Python
    python3 \
    python3-pip \
    python3-venv \
    # Shell utilities
    bash-completion \
    fzf \
    ripgrep \
    fd-find \
    # Network tools
    openssh-client \
    ca-certificates \
    # For some language servers and tools
    unzip \
    # gosu for privilege de-escalation in entrypoint
    gosu \
    # Network firewall (--firewall mode)
    iptables \
    squid \
    && rm -rf /var/lib/apt/lists/*

# Install yq for YAML processing (architecture-aware)
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL -o /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/latest/download/yq_linux_${ARCH}" && \
    chmod +x /usr/local/bin/yq

# Install GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg && \
    chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null && \
    apt-get update && apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# Install Docker CE (for Docker-in-Docker support)
RUN curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg && \
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable" \
    > /etc/apt/sources.list.d/docker.list && \
    apt-get update && apt-get install -y --no-install-recommends \
    docker-ce \
    docker-ce-cli \
    containerd.io \
    && rm -rf /var/lib/apt/lists/*

# Install AI CLI (default: Claude Code)
# The installer may create a symlink; cp -L follows it to copy the real binary.
ARG AI_BINARY="claude"
ARG AI_INSTALL_CMD="curl -fsSL https://claude.ai/install.sh | bash && cp -L /root/.local/bin/claude /usr/local/bin/claude && chmod +x /usr/local/bin/claude && /usr/local/bin/claude --version"
RUN bash -c "${AI_INSTALL_CMD}"

# Build-time extension installer
# Extensions live in extensions/<name>/build.sh
# Build context is the project root, so paths are relative to it.
COPY extensions/ /tmp/ext-builds/
ARG INSTALL_EXTENSIONS=""
ARG DOTNET_CHANNEL="LTS"
ARG GO_VERSION=""
RUN if [ -n "$INSTALL_EXTENSIONS" ]; then \
    IFS=','; for ext in $INSTALL_EXTENSIONS; do \
        script="/tmp/ext-builds/${ext}/build.sh"; \
        if [ -f "$script" ]; then \
            echo "[mittens] Installing extension: $ext" && \
            bash "$script" || exit 1; \
        else \
            echo "[mittens] Warning: no build script for extension '$ext'" >&2; \
        fi; \
    done; \
fi && rm -rf /tmp/ext-builds/

# Create a non-root user matching typical host UID
# The node base image has a 'node' user at UID 1000 — if the requested UID
# collides, rename that user instead of silently failing.
ARG AI_USERNAME="claude"
ARG USER_ID=1000
ARG GROUP_ID=1000
RUN existing_user=$(getent passwd ${USER_ID} | cut -d: -f1) && \
    if [ -n "$existing_user" ] && [ "$existing_user" != "${AI_USERNAME}" ]; then \
        usermod -l "${AI_USERNAME}" -d "/home/${AI_USERNAME}" -m "$existing_user" && \
        existing_group=$(getent group ${GROUP_ID} | cut -d: -f1) && \
        if [ -n "$existing_group" ] && [ "$existing_group" != "${AI_USERNAME}" ]; then \
            groupmod -n "${AI_USERNAME}" "$existing_group"; \
        fi; \
    elif [ -z "$existing_user" ]; then \
        groupadd -g ${GROUP_ID} "${AI_USERNAME}" 2>/dev/null || true && \
        useradd -m -u ${USER_ID} -g ${GROUP_ID} -s /bin/bash "${AI_USERNAME}"; \
    fi && \
    usermod -aG docker "${AI_USERNAME}" 2>/dev/null || true

# Create directories for mounts and config
ARG AI_CONFIG_DIR=".claude"
RUN mkdir -p /home/${AI_USERNAME}/${AI_CONFIG_DIR} /home/${AI_USERNAME}/.config /mnt/claude-config && \
    chown -R ${USER_ID}:${GROUP_ID} /home/${AI_USERNAME} /mnt/claude-config

# Make AI_USERNAME available at runtime for entrypoint.sh
ENV AI_USERNAME=${AI_USERNAME}

# Add squid config and entrypoint
# These files are under container/ in the source tree; build context is the project root.
COPY container/squid.conf /etc/squid/squid.conf
COPY container/mcp-domains.conf /etc/mittens/mcp-domains.conf
RUN mkdir -p /run/squid && chown proxy:proxy /run/squid

COPY container/clipboard-shim.sh /usr/local/bin/xclip
RUN chmod +x /usr/local/bin/xclip

COPY container/open-url.sh /usr/local/bin/xdg-open
RUN chmod +x /usr/local/bin/xdg-open

COPY container/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

COPY container/cred-sync.sh /usr/local/bin/cred-sync.sh
RUN chmod +x /usr/local/bin/cred-sync.sh

COPY container/notify.sh /usr/local/bin/notify.sh
RUN chmod +x /usr/local/bin/notify.sh

# NOTE: No USER directive here — entrypoint.sh handles the root→claude
# privilege drop via gosu, which is needed for optional DinD (dockerd
# must start as root). When DinD is disabled the entrypoint still drops
# to the claude user immediately, so there is no security regression.

WORKDIR /workspace

ENV TERM=xterm-256color
ENV SHELL=/bin/bash

ENTRYPOINT ["entrypoint.sh"]
CMD ["claude"]
