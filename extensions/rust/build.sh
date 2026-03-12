#!/usr/bin/env bash
# Build-time install script for the rust extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "rust".
set -euo pipefail

TOOLCHAIN="${RUST_TOOLCHAIN:-stable}"

export RUSTUP_HOME=/usr/local/rustup
export CARGO_HOME=/usr/local/cargo

echo "[mittens] Installing Rust toolchain: $TOOLCHAIN"

curl -fsSL https://sh.rustup.rs | sh -s -- \
    -y \
    --default-toolchain "$TOOLCHAIN" \
    --profile minimal \
    --no-modify-path

# Make cargo/rustc available system-wide
cat > /etc/profile.d/rust.sh <<'EOF'
export RUSTUP_HOME=/usr/local/rustup
export CARGO_HOME=/home/claude/.cargo
export PATH=$PATH:/usr/local/cargo/bin
EOF

# Symlink binaries so they're on PATH without profile sourcing during build
ln -sf /usr/local/cargo/bin/* /usr/local/bin/ 2>/dev/null || true

# Pre-create .cargo so Docker bind mounts don't create it as root
# (ownership is fixed by the chown -R in Dockerfile after useradd)
mkdir -p /home/claude/.cargo/registry
