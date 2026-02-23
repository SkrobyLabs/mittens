#!/usr/bin/env bash
# Build-time install script for the gcp extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "gcp".
set -euo pipefail

curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg \
    | gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" \
    > /etc/apt/sources.list.d/google-cloud-sdk.list
apt-get update && apt-get install -y --no-install-recommends google-cloud-cli && rm -rf /var/lib/apt/lists/*
