#!/usr/bin/env bash
# Build-time install script for the aws extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "aws".
set -euo pipefail

ARCH=$(uname -m)
curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${ARCH}.zip" -o /tmp/awscliv2.zip
unzip -q /tmp/awscliv2.zip -d /tmp
/tmp/aws/install
rm -rf /tmp/awscliv2.zip /tmp/aws
