#!/usr/bin/env bash
# Build-time install script for the opentofu extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "opentofu".
set -euo pipefail

ARCH=$(dpkg --print-architecture)

TOFU_VERSION=$(curl -fsSL https://api.github.com/repos/opentofu/opentofu/releases/latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')
echo "[mittens] Installing OpenTofu v${TOFU_VERSION} (${ARCH})"
curl -fsSL "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_${ARCH}.tar.gz" -o /tmp/tofu.tar.gz
tar -xzf /tmp/tofu.tar.gz -C /usr/local/bin tofu
chmod +x /usr/local/bin/tofu
rm /tmp/tofu.tar.gz
