#!/usr/bin/env bash
# Устанавливает собранные бинарники и синхронизирует конфиг/скиллы,
# затем перезапускает сервисы tg-lurk-ai-bot.
# Запускается от root через sudo пользователем deploy.
# Аргумент: каталог со staged-файлами (по умолчанию /tmp/deploy).
set -euo pipefail

SRC="${1:-/tmp/deploy}"
DEST="/opt/tg-lurk-ai-bot"

# Бинарники.
for name in bot backend mcp; do
  if [ -f "$SRC/$name" ]; then
    install -m 0755 -o lurk_ai_bot -g users "$SRC/$name" "$DEST/bin/$name"
    echo "installed $name"
  fi
done

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

systemctl restart backend bot opencode-serve
echo "services restarted"
