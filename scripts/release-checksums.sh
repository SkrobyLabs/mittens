#!/bin/sh
# Hash only this release's artifacts, not stale files in the output directory.
set -eu

if [ "$#" -lt 2 ]; then
    echo "usage: release-checksums.sh <directory> <artifact>..." >&2
    exit 1
fi
release_dir=$1
shift
cd "$release_dir"
manifest_tmp=$(mktemp SHA256SUMS.XXXXXX)
trap 'rm -f "$manifest_tmp"' 0 1 2 15
if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- "$@" > "$manifest_tmp"
elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -- "$@" > "$manifest_tmp"
else
    echo "release checksums require sha256sum or shasum" >&2
    exit 1
fi
mv "$manifest_tmp" SHA256SUMS
