#!/usr/bin/env bash
# Build-time install script for the helmfile extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "helmfile".
# Requires the 'helm' extension to have run first (alphabetical ordering guarantees this).
set -euo pipefail

ARCH=$(dpkg --print-architecture)

if ! command -v helm >/dev/null 2>&1; then
    echo "[mittens] helmfile extension requires the 'helm' extension to be enabled" >&2
    exit 1
fi

install_helm_plugin() {
    local name="$1"
    local repo="$2"

    if helm plugin list 2>/dev/null | awk '{print $1}' | grep -qx "$name"; then
        echo "[mittens] Helm plugin ${name} already installed"
        return
    fi

    echo "[mittens] Installing Helm plugin: ${name}"
    helm plugin install "$repo" --verify=false
}

# helmfile
if command -v helmfile >/dev/null 2>&1; then
    echo "[mittens] helmfile already installed"
else
    HELMFILE_VERSION=$(curl -fsSL https://api.github.com/repos/helmfile/helmfile/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')
    echo "[mittens] Installing helmfile v${HELMFILE_VERSION} (${ARCH})"
    curl -fsSL "https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_linux_${ARCH}.tar.gz" -o /tmp/helmfile.tar.gz
    tar -xzf /tmp/helmfile.tar.gz -C /usr/local/bin helmfile
    chmod +x /usr/local/bin/helmfile
    rm /tmp/helmfile.tar.gz
fi

# sops (for helm-secrets)
if command -v sops >/dev/null 2>&1; then
    echo "[mittens] sops already installed"
else
    SOPS_VERSION=$(curl -fsSL https://api.github.com/repos/getsops/sops/releases/latest | grep '"tag_name"' | sed -E 's/.*"(v[^"]+)".*/\1/')
    echo "[mittens] Installing sops ${SOPS_VERSION} (${ARCH})"
    curl -fsSL "https://github.com/getsops/sops/releases/download/${SOPS_VERSION}/sops-${SOPS_VERSION}.linux.${ARCH}" -o /usr/local/bin/sops
    chmod +x /usr/local/bin/sops
fi

# age (default sops backend; works with sops-encrypted secrets.yaml)
if command -v age >/dev/null 2>&1 && command -v age-keygen >/dev/null 2>&1; then
    echo "[mittens] age already installed"
else
    AGE_VERSION=$(curl -fsSL https://api.github.com/repos/FiloSottile/age/releases/latest | grep '"tag_name"' | sed -E 's/.*"(v[^"]+)".*/\1/')
    echo "[mittens] Installing age ${AGE_VERSION} (${ARCH})"
    curl -fsSL "https://github.com/FiloSottile/age/releases/download/${AGE_VERSION}/age-${AGE_VERSION}-linux-${ARCH}.tar.gz" -o /tmp/age.tar.gz
    tar -xzf /tmp/age.tar.gz -C /tmp
    cp /tmp/age/age /tmp/age/age-keygen /usr/local/bin/
    chmod +x /usr/local/bin/age /usr/local/bin/age-keygen
    rm -rf /tmp/age.tar.gz /tmp/age
fi

# Install helm plugins system-wide so they are visible to every user in the
# container. extension.yaml sets HELM_DATA_HOME/HELM_PLUGINS to this path.
export HELM_DATA_HOME=/opt/helm/data
mkdir -p "${HELM_DATA_HOME}"

echo "[mittens] Installing helm-diff plugin"
install_helm_plugin diff https://github.com/databus23/helm-diff

echo "[mittens] Installing helm-secrets plugin"
install_helm_plugin secrets https://github.com/jkroepke/helm-secrets

chmod -R a+rX /opt/helm
