#!/usr/bin/env bash
# Build-time install script for the go extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "go".
set -euo pipefail

GO_VERSION="${GO_VERSION:-1.23}"

# Append .0 if version is just major.minor (e.g. 1.24 -> 1.24.0)
# Go download URLs require the patch version.
if [[ "$GO_VERSION" =~ ^[0-9]+\.[0-9]+$ ]]; then
    GO_VERSION="${GO_VERSION}.0"
fi

# Detect architecture
if command -v dpkg &>/dev/null; then
    ARCH="$(dpkg --print-architecture)"
else
    case "$(uname -m)" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        *)       echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
    esac
fi

TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz"
echo "[mittens] Downloading Go ${GO_VERSION} (${ARCH})..."

curl -fsSL "https://go.dev/dl/${TARBALL}" -o "/tmp/${TARBALL}"
tar -C /usr/local -xzf "/tmp/${TARBALL}"
rm "/tmp/${TARBALL}"

# Make Go available system-wide
cat > /etc/profile.d/golang.sh <<'EOF'
export GOPATH=/home/claude/go
export PATH=$PATH:/usr/local/go/bin:$GOPATH/bin
EOF

# Pre-create GOPATH so Docker bind mounts don't create it as root
# (ownership is fixed by the chown -R in Dockerfile after useradd)
mkdir -p /home/claude/go/pkg/mod
