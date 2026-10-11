#!/usr/bin/env bash
#
# Deploy script for tg-lurk-ai-bot.
# Run on the VDS as root (or with sudo). Idempotent — safe to re-run.
#
#   sudo bash deploy.sh
#
# First run installs Go/Git, builds binaries, installs OpenCode and creates the
# env file, then stops so you can fill in the secrets. After editing the env
# file, run it again to install and start the systemd services.

set -euo pipefail

REPO_URL="https://github.com/aesvechnikov/tg-lurk-ai-bot.git"
INSTALL_DIR="/opt/tg-lurk-ai-bot"
SERVICE_GROUP="users"
SERVICE_USER="lurk_ai_bot"
SERVICE_HOME="/home/lurk_ai_bot"
ENV_FILE="/etc/tg-lurk-ai-bot/env"
GO_VERSION="${GO_VERSION:-1.26.2}"

log()  { printf '==> %s\n' "$*"; }
warn() { printf '!! %s\n' "$*" >&2; }
die()  { printf 'ERROR: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root (e.g. sudo bash deploy.sh)"
if ! command -v curl >/dev/null 2>&1; then
  apt-get update
  apt-get install -y curl
fi

# ---------------------------------------------------------------------------
# 1. Git
# ---------------------------------------------------------------------------
if ! command -v git >/dev/null 2>&1; then
  log "installing git"
  apt-get update
  apt-get install -y git
fi

# ---------------------------------------------------------------------------
# 2. Go
# ---------------------------------------------------------------------------
if ! command -v go >/dev/null 2>&1; then
  case "$(dpkg --print-architecture)" in
    amd64) go_arch=amd64 ;;
    arm64) go_arch=arm64 ;;
    *)     die "unsupported architecture: $(dpkg --print-architecture)" ;;
  esac
  tarball="go${GO_VERSION}.linux-${go_arch}.tar.gz"
  log "installing Go ${GO_VERSION} (${go_arch})"
  curl -fsSL "https://go.dev/dl/${tarball}" -o "/tmp/${tarball}"
  rm -rf /usr/local/go
  tar -C /usr/local -xzf "/tmp/${tarball}"
fi
ln -sf /usr/local/go/bin/go /usr/local/bin/go
ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
export PATH="/usr/local/go/bin:$PATH"
log "go $(go version | awk '{print $3}')"

# ---------------------------------------------------------------------------
# 3. Service group and user
# ---------------------------------------------------------------------------
if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
  log "creating group ${SERVICE_GROUP}"
  groupadd "$SERVICE_GROUP"
fi
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  log "creating user ${SERVICE_USER}"
  useradd --system --gid "$SERVICE_GROUP" --create-home --home-dir "$SERVICE_HOME" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# ---------------------------------------------------------------------------
# 4. Clone and build
# ---------------------------------------------------------------------------
# The source tree stays owned by the user running this script (root); the
# service user only needs read access. `-c safe.directory=...` is passed inline
# so git trusts the dir regardless of ownership or global config state.
if [ -d "$INSTALL_DIR/.git" ]; then
  log "updating repository"
  if ! git -C "$INSTALL_DIR" -c "safe.directory=$INSTALL_DIR" pull --ff-only; then
    warn "pull failed; falling back to a fresh clone"
    rm -rf "$INSTALL_DIR"
    git clone "$REPO_URL" "$INSTALL_DIR"
  fi
else
  log "cloning repository"
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
chmod -R a+rX "$INSTALL_DIR"

if [ "${SKIP_BUILD:-0}" = "1" ]; then
  log "skipping build (SKIP_BUILD=1): using pre-built binaries in ${INSTALL_DIR}/bin"
else
  log "building binaries"
  mkdir -p "$INSTALL_DIR/bin"
  (
    cd "$INSTALL_DIR"
    export PATH="/usr/local/go/bin:$PATH"
    export GOTOOLCHAIN=auto
    # -buildvcs=false: don't shell out to git status (ownership mismatch).
    # -p=1: serialize package builds to lower peak memory. Note that compiling
    # github.com/gotd/td/tg still needs >1.5GB — on a 2GB VDS prefer SKIP_BUILD
    # with binaries cross-compiled elsewhere, or add swap.
    export GOFLAGS="-buildvcs=false -p=1"
    go build -o bin/bot     ./cmd/bot
    go build -o bin/backend ./cmd/backend
    go build -o bin/mcp     ./cmd/mcp
  )
fi
mkdir -p "$INSTALL_DIR/data"
# data/ holds the SQLite DB and encrypted sessions — the only thing the
# service user must be able to write.
chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR/data"

# ---------------------------------------------------------------------------
# 5. ffmpeg + Vosk model (voice messages)
# ---------------------------------------------------------------------------
if ! command -v ffmpeg >/dev/null 2>&1 || ! command -v unzip >/dev/null 2>&1; then
  log "installing ffmpeg and unzip"
  apt-get install -y ffmpeg unzip
fi

VOSK_MODEL_DIR="${INSTALL_DIR}/models/vosk"
if [ ! -d "$VOSK_MODEL_DIR" ]; then
  log "downloading Vosk model (small-ru)"
  mkdir -p "${INSTALL_DIR}/models"
  curl -fsSL "https://alphacephei.com/vosk/models/vosk-model-small-ru-0.22.zip" -o /tmp/vosk-model.zip
  unzip -o -q /tmp/vosk-model.zip -d "${INSTALL_DIR}/models/"
  mv "${INSTALL_DIR}/models/vosk-model-small-ru-0.22" "$VOSK_MODEL_DIR"
fi
chown -R "$SERVICE_USER":"$SERVICE_USER" "${INSTALL_DIR}/models"

# ---------------------------------------------------------------------------
# 6. OpenCode
# ---------------------------------------------------------------------------
if ! command -v opencode >/dev/null 2>&1; then
  log "installing OpenCode"
  sudo -u "$SERVICE_USER" env HOME="$SERVICE_HOME" bash -c \
    'curl -fsSL https://opencode.ai/install | bash' || true
  for p in \
    "$SERVICE_HOME/.opencode/bin/opencode" \
    "$SERVICE_HOME/.local/bin/opencode" \
    "$SERVICE_HOME/.opencode/current/bin/opencode"; do
    if [ -x "$p" ]; then
      ln -sf "$p" /usr/local/bin/opencode
      break
    fi
  done
fi
command -v opencode >/dev/null 2>&1 || die "could not locate the opencode binary"
log "opencode $(opencode --version 2>/dev/null || echo 'installed')"

# ---------------------------------------------------------------------------
# 7. Env file
# ---------------------------------------------------------------------------
if [ ! -f "$ENV_FILE" ]; then
  log "creating env file ${ENV_FILE}"
  mkdir -p "$(dirname "$ENV_FILE")"
  cp "$INSTALL_DIR/.env.example" "$ENV_FILE"
  # Replace the empty SESSION_KEY line with a freshly generated key.
  sed -i "s|^SESSION_KEY=$|SESSION_KEY=$(openssl rand -hex 32)|" "$ENV_FILE"
  chmod 600 "$ENV_FILE"
  chown root:root "$ENV_FILE"
  cat <<EOF

--------------------------------------------------------------
Env file created: ${ENV_FILE}
Fill in these values, then re-run this script:
  - BOT_TOKEN          (from @BotFather)
  - TG_API_ID          (from https://my.telegram.org/apps)
  - TG_API_HASH        (from https://my.telegram.org/apps)
  - OPENCODE_SERVER_PASSWORD
  - DEEPSEEK_API_KEY
SESSION_KEY was generated for you.
--------------------------------------------------------------
EOF
  exit 0
fi

# ---------------------------------------------------------------------------
# 8. Validate env and install services
# ---------------------------------------------------------------------------
set -a; . "$ENV_FILE"; set +a
missing=""
for v in BOT_TOKEN TG_API_ID TG_API_HASH SESSION_KEY OPENCODE_SERVER_PASSWORD DEEPSEEK_API_KEY; do
  if [ -z "${!v:-}" ]; then missing="${missing} ${v}"; fi
done
if [ -n "$missing" ]; then
  die "missing values in ${ENV_FILE}:${missing}"
fi

log "installing systemd units"
cp "$INSTALL_DIR/deploy/"*.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now backend.service opencode-serve.service bot.service

log "done. status:"
systemctl --no-pager --lines=0 status backend.service opencode-serve.service bot.service \
  || true
cat <<EOF

Bot is running. Open Telegram and send /start to your bot.
If a service fails:  journalctl -u <service> -e
EOF
