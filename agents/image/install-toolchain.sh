#!/bin/sh
# Installs what the golden-path scaffolds need to develop and test, on Alpine.
# Shared by both image targets so the builder and its sandbox match exactly.
# Everything comes from Alpine's signed package repositories; nothing is
# downloaded unverified. uv is copied from its digest-pinnable image in the
# Dockerfile.
set -eu

apk upgrade --no-cache
apk add --no-cache \
  bash ca-certificates curl git jq make ripgrep \
  nodejs npm \
  python3 py3-pip \
  go \
  shellcheck yamllint

# Scaffolds declare requires-python >= 3.12 and run on the system python3.
# Stop uv from downloading interpreters so its only egress is PyPI.
mkdir -p /etc/uv
printf 'python-downloads = "never"\npython-preference = "only-system"\n' > /etc/uv/uv.toml

node --version
npm --version
python3 --version
go version
git --version
