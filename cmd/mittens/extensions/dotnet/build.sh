#!/usr/bin/env bash
# Build-time install script for the dotnet extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "dotnet".
# DOTNET_CHANNEL may be a single channel (e.g. "8") or comma-separated (e.g. "8,10").
set -euo pipefail

CHANNELS="${DOTNET_CHANNEL:-LTS}"

curl -fsSL https://dot.net/v1/dotnet-install.sh -o /tmp/dotnet-install.sh
chmod +x /tmp/dotnet-install.sh

IFS=',' read -ra CH_ARRAY <<< "$CHANNELS"
for ch in "${CH_ARRAY[@]}"; do
    # Append .0 for bare major versions (e.g. 8 -> 8.0)
    if [[ "$ch" =~ ^[0-9]+$ ]]; then
        ch="${ch}.0"
    fi
    echo "[dotnet] Installing channel: $ch"
    /tmp/dotnet-install.sh --channel "$ch" --install-dir /usr/share/dotnet
done

rm /tmp/dotnet-install.sh

# Symlink only needs to be created once (shared install dir)
if [ ! -L /usr/local/bin/dotnet ]; then
    ln -s /usr/share/dotnet/dotnet /usr/local/bin/dotnet
fi

echo 'export DOTNET_ROOT=/usr/share/dotnet' >> /etc/profile.d/dotnet.sh
echo 'export PATH=$PATH:/usr/share/dotnet' >> /etc/profile.d/dotnet.sh

# Pre-create .nuget so Docker bind mounts don't create it as root
# (ownership is fixed by the chown -R in Dockerfile after useradd)
mkdir -p /home/claude/.nuget
