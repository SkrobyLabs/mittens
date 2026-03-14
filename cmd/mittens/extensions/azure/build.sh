#!/usr/bin/env bash
# Build-time install script for the azure extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "azure".
set -euo pipefail

curl -fsSL https://aka.ms/InstallAzureCLIDeb | bash
