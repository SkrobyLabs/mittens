#!/usr/bin/env bash
# Build-time install script for the trivy extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "trivy".
set -euo pipefail

TRIVY_VERSION="${TRIVY_VERSION:-0.69.3}"

# Detect architecture — dpkg preferred, uname -m as fallback.
if command -v dpkg >/dev/null 2>&1; then
    ARCH=$(dpkg --print-architecture)
else
    ARCH=$(uname -m)
fi

# Trivy uses non-standard arch naming in release assets.
case "$ARCH" in
    amd64|x86_64) TRIVY_ARCH="64bit" ;;
    arm64|aarch64) TRIVY_ARCH="ARM64" ;;
    *) echo "[mittens] Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

echo "[mittens] Installing Trivy ${TRIVY_VERSION} (${TRIVY_ARCH})"
curl -fsSL "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-${TRIVY_ARCH}.tar.gz" \
    -o /tmp/trivy.tar.gz
tar -xzf /tmp/trivy.tar.gz -C /tmp trivy
cp /tmp/trivy /usr/local/bin/trivy
chmod +x /usr/local/bin/trivy
rm -f /tmp/trivy.tar.gz /tmp/trivy

echo "[mittens] Trivy installed: $(trivy --version)"
