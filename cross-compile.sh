#!/usr/bin/env bash
#
# Cross-compile the three tg-lurk-ai-bot binaries for the target server.
# Run this on a machine with enough RAM (e.g. your Mac) — compiling
# github.com/gotd/td/tg needs >2GB, which a 2GB VDS may not have.
#
# Usage:
#   ./cross-compile.sh                         # linux/amd64 -> dist/
#   TARGET_GOARCH=arm64 ./cross-compile.sh     # override architecture
#   OUT_DIR=release ./cross-compile.sh

set -euo pipefail

# Use TARGET_* names (not GOOS/GOARCH) so an existing GOOS/GOARCH in the
# environment cannot leak into the build.
TARGET_GOOS="${TARGET_GOOS:-linux}"
TARGET_GOARCH="${TARGET_GOARCH:-amd64}"
OUT_DIR="${OUT_DIR:-dist}"

cd "$(dirname "$0")"
mkdir -p "$OUT_DIR"

echo "==> building for ${TARGET_GOOS}/${TARGET_GOARCH} into ${OUT_DIR}/"

build() {
  local src="$1"
  local name="$2"
  GOOS="$TARGET_GOOS" GOARCH="$TARGET_GOARCH" CGO_ENABLED=0 GOFLAGS="-buildvcs=false" \
    go build -o "$OUT_DIR/$name" "./cmd/$src"
}

build bot tg-lurk-ai-bot
build backend tg-lurk-ai-bot-backend
build mcp tg-lurk-ai-bot-mcp

echo "==> verifying architecture (must be ELF x86-64 / ELF aarch64):"
file "$OUT_DIR"/tg-lurk-ai-bot "$OUT_DIR"/tg-lurk-ai-bot-backend "$OUT_DIR"/tg-lurk-ai-bot-mcp

cat <<EOF

==> Done. Upload ONLY these three files to the server:

  scp ${OUT_DIR}/tg-lurk-ai-bot ${OUT_DIR}/tg-lurk-ai-bot-backend ${OUT_DIR}/tg-lurk-ai-bot-mcp user@SERVER:/tmp/

Then on the server:

  sudo systemctl stop tg-lurk-ai-bot-backend tg-lurk-ai-bot tg-lurk-ai-bot-opencode
  sudo cp /tmp/tg-lurk-ai-bot /tmp/tg-lurk-ai-bot-backend /tmp/tg-lurk-ai-bot-mcp /opt/tg-lurk-ai-bot/bin/
  sudo chmod +x /opt/tg-lurk-ai-bot/bin/tg-lurk-ai-bot /opt/tg-lurk-ai-bot/bin/tg-lurk-ai-bot-backend /opt/tg-lurk-ai-bot/bin/tg-lurk-ai-bot-mcp
  sudo SKIP_BUILD=1 bash deploy.sh
EOF
