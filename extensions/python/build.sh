#!/usr/bin/env bash
# Build-time install script for the python extension.
# Executed inside the Docker image build when INSTALL_EXTENSIONS includes "python".
set -euo pipefail

PYTHON_VERSION="${PYTHON_VERSION:-3.9}"

echo "[mittens] Installing Python ${PYTHON_VERSION} from source..."

# Install build dependencies
apt-get update -qq
apt-get install -y --no-install-recommends \
    build-essential \
    libssl-dev \
    zlib1g-dev \
    libbz2-dev \
    libreadline-dev \
    libsqlite3-dev \
    libffi-dev \
    liblzma-dev \
    libncursesw5-dev \
    tk-dev \
    uuid-dev

# Find latest patch version for the requested minor version
PATCH_URL="https://www.python.org/ftp/python/"
LATEST_PATCH=$(curl -fsSL "${PATCH_URL}" \
    | grep -oP "href=\"${PYTHON_VERSION}\.\d+/\"" \
    | sed 's/href="//;s/\///' \
    | sort -t. -k3 -n \
    | tail -1)

if [ -z "$LATEST_PATCH" ]; then
    echo "[mittens] ERROR: Could not find a patch release for Python ${PYTHON_VERSION}" >&2
    exit 1
fi

FULL_VERSION="$LATEST_PATCH"
echo "[mittens] Resolved Python ${PYTHON_VERSION} -> ${FULL_VERSION}"

TARBALL="Python-${FULL_VERSION}.tgz"
curl -fsSL "https://www.python.org/ftp/python/${FULL_VERSION}/${TARBALL}" -o "/tmp/${TARBALL}"
cd /tmp
tar xzf "${TARBALL}"
cd "Python-${FULL_VERSION}"

echo "[mittens] Configuring Python ${FULL_VERSION}..."
./configure --prefix="/usr/local/python${PYTHON_VERSION}" \
    --enable-shared \
    --with-ensurepip=install \
    LDFLAGS="-Wl,-rpath,/usr/local/python${PYTHON_VERSION}/lib" \
    > /dev/null 2>&1

echo "[mittens] Building Python ${FULL_VERSION} (this may take a few minutes)..."
make -j"$(nproc)" > /dev/null 2>&1
make altinstall > /dev/null 2>&1

# Create symlinks
PYBIN="/usr/local/python${PYTHON_VERSION}/bin"
ln -sf "${PYBIN}/python${PYTHON_VERSION}" "${PYBIN}/python3"
ln -sf "${PYBIN}/python${PYTHON_VERSION}" "${PYBIN}/python"
ln -sf "${PYBIN}/pip${PYTHON_VERSION}" "${PYBIN}/pip3"
ln -sf "${PYBIN}/pip${PYTHON_VERSION}" "${PYBIN}/pip"

# Make Python available system-wide (takes priority over system python)
cat > /etc/profile.d/python.sh <<EOF
export PATH=/usr/local/python${PYTHON_VERSION}/bin:\$PATH
EOF

# Pre-create pip cache so Docker bind mounts don't create it as root
mkdir -p /home/claude/.cache/pip

# Clean up build artifacts and dependencies to keep image lean
cd /
rm -rf /tmp/Python-* /tmp/*.tgz
apt-get purge -y --auto-remove \
    build-essential \
    libssl-dev \
    zlib1g-dev \
    libbz2-dev \
    libreadline-dev \
    libsqlite3-dev \
    libffi-dev \
    liblzma-dev \
    libncursesw5-dev \
    tk-dev \
    uuid-dev
apt-get clean
rm -rf /var/lib/apt/lists/*

echo "[mittens] Python ${FULL_VERSION} installed at /usr/local/python${PYTHON_VERSION}"
