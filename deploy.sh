#!/usr/bin/env bash
#
# Deploy script for candle-lurk-tg-bot.
# Run on the VDS as root (or with sudo). Idempotent — safe to re-run.
#
#   sudo bash deploy.sh
#
# First run installs Go/Git, builds binaries, installs OpenCode and creates the
# env file, then stops so you can fill in the secrets. After editing the env
# file, run it again to install and start the systemd services.

set -euo pipefail

REPO_URL="https://github.com/aesvechnikov/candle-lurk-tg-bot.git"
INSTALL_DIR="/opt/candle-lurk-tg-bot"
SERVICE_USER="candle"
SERVICE_HOME="/home/candle"
ENV_FILE="/etc/candle-lurk-tg-bot/env"
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
# 3. Service user
# ---------------------------------------------------------------------------
if ! id -u "$SERVICE_USER" >/dev/null 2>&1; then
  log "creating user ${SERVICE_USER}"
  useradd --system --create-home --home-dir "$SERVICE_HOME" --shell /usr/sbin/nologin "$SERVICE_USER"
fi

# ---------------------------------------------------------------------------
# 4. Clone and build
# ---------------------------------------------------------------------------
if [ -d "$INSTALL_DIR/.git" ]; then
  log "updating repository"
  git -C "$INSTALL_DIR" pull --ff-only
else
  log "cloning repository"
  git clone "$REPO_URL" "$INSTALL_DIR"
fi
chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR"

log "building binaries"
mkdir -p "$INSTALL_DIR/bin"
(
  cd "$INSTALL_DIR"
  export PATH="/usr/local/go/bin:$PATH"
  export GOTOOLCHAIN=auto
  go build -o bin/bot     ./cmd/bot
  go build -o bin/backend ./cmd/backend
  go build -o bin/mcp     ./cmd/mcp
)
mkdir -p "$INSTALL_DIR/data"
chown -R "$SERVICE_USER":"$SERVICE_USER" "$INSTALL_DIR/bin" "$INSTALL_DIR/data"

# ---------------------------------------------------------------------------
# 5. OpenCode
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
# 6. Env file
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
# 7. Validate env and install services
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
