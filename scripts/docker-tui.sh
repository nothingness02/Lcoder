#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

exec docker compose -f docker/manual/docker-compose.yml run --rm lcoder tui "$@"
