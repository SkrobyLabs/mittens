#!/usr/bin/env bash
# Build-time install script for the kubectl extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "kubectl".
set -euo pipefail

ARCH=$(dpkg --print-architecture)
KUBECTL_VERSION=$(curl -fsSL https://dl.k8s.io/release/stable.txt)
curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl" \
    -o /usr/local/bin/kubectl
chmod +x /usr/local/bin/kubectl
