#!/bin/bash
# Install redis-tools in the container image.
set -e
apt-get update -qq && apt-get install -y -qq redis-tools >/dev/null 2>&1
echo "[mittens] redis-tools installed"
