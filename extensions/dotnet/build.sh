#!/usr/bin/env bash
# Build-time install script for the dotnet extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "dotnet".
set -euo pipefail

CHANNEL="${DOTNET_CHANNEL:-LTS}"

curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh
chmod +x /tmp/dotnet-install.sh
/tmp/dotnet-install.sh --channel "$CHANNEL" --install-dir /usr/share/dotnet
ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet
rm /tmp/dotnet-install.sh
echo 'export DOTNET_ROOT=/usr/share/dotnet' >> /etc/profile.d/dotnet.sh
echo 'export PATH=$PATH:/usr/share/dotnet' >> /etc/profile.d/dotnet.sh

# Pre-create .nuget so Docker bind mounts don't create it as root
# (ownership is fixed by the chown -R in Dockerfile after useradd)
mkdir -p /home/claude/.nuget
