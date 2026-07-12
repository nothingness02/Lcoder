#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

mkdir -p "${REPO_ROOT}/bin"

echo "Cross-compiling lcoder-linux..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build \
  -ldflags="-s -w" \
  -o "${REPO_ROOT}/bin/lcoder-linux" \
  "${REPO_ROOT}/cmd/lcoder"

cd "${SCRIPT_DIR}"
echo "Building manual container image..."
docker compose build
