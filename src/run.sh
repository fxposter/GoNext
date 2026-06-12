#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

go build -C "$DIR" -o "$DIR/gonext" ./cmd/gonext
exec "$DIR/gonext" run "$@"
