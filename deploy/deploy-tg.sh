#!/usr/bin/env bash
# Устанавливает собранные бинарники и синхронизирует конфиг/скиллы,
# затем перезапускает сервисы tg-lurk-ai-bot.
# Запускается от root через sudo пользователем deploy.
# Аргумент: каталог со staged-файлами (по умолчанию /tmp/deploy).
set -euo pipefail

SRC="${1:-/tmp/deploy}"
DEST="/opt/tg-lurk-ai-bot"

# Self-update: CI stages a fresh copy of this script as $SRC/deploy-tg.sh. The
# deploy user's sudo is limited to running /usr/local/bin/deploy-tg, so the only
# way to refresh it is from inside this script (which runs as root). Install a
# newer copy and re-exec so the updated code takes effect for this run too.
if [ -f "$SRC/deploy-tg.sh" ] && ! cmp -s "$SRC/deploy-tg.sh" /usr/local/bin/deploy-tg; then
  install -m 0755 "$SRC/deploy-tg.sh" /usr/local/bin/deploy-tg
  echo "updated /usr/local/bin/deploy-tg"
  exec /usr/local/bin/deploy-tg "$SRC"
fi

# Бинарники.
for name in tg-lurk-ai-bot tg-lurk-ai-bot-backend tg-lurk-ai-bot-mcp tg-lurk-ai-bot-stt; do
  if [ -f "$SRC/$name" ]; then
    install -m 0755 -o lurk_ai_bot -g users "$SRC/$name" "$DEST/bin/$name"
    echo "installed $name"
  fi
done

# libvosk.so (нужен stt-сервису в рантайме).
if [ -f "$SRC/libvosk.so" ]; then
  install -m 0644 "$SRC/libvosk.so" /usr/local/lib/libvosk.so
  ldconfig || true
  echo "installed libvosk.so"
fi

# Конфиг и правила.
for f in opencode.json AGENTS.md; do
  if [ -f "$SRC/$f" ]; then
    install -m 0644 -o lurk_ai_bot -g users "$SRC/$f" "$DEST/$f"
    echo "installed $f"
  fi
done

# Агент и скиллы (.opencode/).
if [ -d "$SRC/.opencode" ]; then
  rm -rf "$DEST/.opencode"
  cp -r "$SRC/.opencode" "$DEST/.opencode"
  chown -R lurk_ai_bot:users "$DEST/.opencode"
  echo "installed .opencode"
fi

# systemd units (ExecStart paths etc.).
for f in "$SRC"/*.service; do
  [ -f "$f" ] || continue
  install -m 0644 "$f" "/etc/systemd/system/$(basename "$f")"
  echo "installed $(basename "$f")"
done
systemctl daemon-reload

systemctl restart tg-lurk-ai-bot-backend tg-lurk-ai-bot tg-lurk-ai-bot-opencode tg-lurk-ai-bot-stt
echo "services restarted"
