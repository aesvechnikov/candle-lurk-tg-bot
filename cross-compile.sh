#!/usr/bin/env bash
#
# Cross-compile the three candle-lurk-tg-bot binaries for the target server.
# Run this on a machine with enough RAM (e.g. your Mac) — compiling
# github.com/gotd/td/tg needs >2GB, which a 2GB VDS may not have.
#
# Usage:
#   ./cross-compile.sh              # linux/amd64 -> dist/
#   GOARCH=arm64 ./cross-compile.sh # override architecture
#   OUT_DIR=release ./cross-compile.sh

set -euo pipefail

GOOS="${GOOS:-linux}"
GOARCH="${GOARCH:-amd64}"
OUT_DIR="${OUT_DIR:-dist}"

cd "$(dirname "$0")"

mkdir -p "$OUT_DIR"

echo "==> building for ${GOOS}/${GOARCH} into ${OUT_DIR}/"

export CGO_ENABLED=0
export GOFLAGS="-buildvcs=false"

go build -o "$OUT_DIR/bot"     ./cmd/bot
go build -o "$OUT_DIR/backend" ./cmd/backend
go build -o "$OUT_DIR/mcp"     ./cmd/mcp

ls -lh "$OUT_DIR"

cat <<EOF

==> Done. Upload ONLY these three files to the server:

  scp ${OUT_DIR}/bot ${OUT_DIR}/backend ${OUT_DIR}/mcp user@SERVER:/tmp/

Then on the server:

  sudo mkdir -p /opt/candle-lurk-tg-bot/bin
  sudo cp /tmp/bot /tmp/backend /tmp/mcp /opt/candle-lurk-tg-bot/bin/
  sudo SKIP_BUILD=1 bash deploy.sh
EOF
