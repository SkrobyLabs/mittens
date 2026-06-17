#!/usr/bin/env bash
# Build-time install script for the docker extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "docker"
# (i.e. when the docker capability is enabled for dind or host-socket mode).
#
# The base Dockerfile adds the AI user to the "docker" group after extensions
# run, so the group created by the docker-ce package below is picked up
# automatically.
set -euo pipefail

echo "[mittens] Installing Docker CE (dind / host-socket support)..."

apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl gnupg

install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg

echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/debian bookworm stable" \
    > /etc/apt/sources.list.d/docker.list

apt-get update -qq
apt-get install -y --no-install-recommends \
    docker-ce \
    docker-ce-cli \
    docker-buildx-plugin \
    containerd.io

apt-get clean
rm -rf /var/lib/apt/lists/*

echo "[mittens] Docker CE installed ($(docker --version 2>/dev/null || echo 'version unavailable'))"
