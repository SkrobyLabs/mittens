#!/usr/bin/env bash
# Build-time install script for the helm extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "helm".
set -euo pipefail

ARCH=$(dpkg --print-architecture)
HELM_VERSION=$(curl -fsSL https://api.github.com/repos/helm/helm/releases/latest | grep '"tag_name"' | sed -E 's/.*"(v[^"]+)".*/\1/')
echo "[mittens] Installing Helm ${HELM_VERSION} (${ARCH})"
curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${ARCH}.tar.gz" -o /tmp/helm.tar.gz
tar -xzf /tmp/helm.tar.gz -C /tmp
cp "/tmp/linux-${ARCH}/helm" /usr/local/bin/helm
chmod +x /usr/local/bin/helm
rm -rf /tmp/helm.tar.gz "/tmp/linux-${ARCH}"
