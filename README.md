# candle-lurk-tg-bot

Telegram-бот, который отвечает на вопросы, ища информацию в ваших Telegram-каналах,
и присылает краткий ответ со ссылками на источники.

Поиск — по точному подстрочному совпадению (так устроен Telegram), поэтому бот
выделяет из вопроса ключевые запросы, ищет их в сохранённых каналах и сводит
результат в лаконичный ответ силами DeepSeek через headless-сервер OpenCode
(агент + скиллы + MCP-инструмент поиска).

## Архитектура

```
пользователь --вопрос--> [bot (telego)] --создаёт сессию--> [opencode serve] (agent answer, skills, DeepSeek)
                                                            |  вызывает инструмент search_tg
                                                            v
                                              [mcp (mcp-go, stdio)] --HTTP--> [backend (gotd, MTProto)]
                                                                              |  сессия пользователя, messages.search
                                                                              v
                                                              результаты + ссылки t.me --> ответ в Telegram
```

Три процесса:

| Бинарь | Роль |
|---|---|
| `bot` | Telegram-фронтенд: команды, вход, вызов OpenCode, отправка ответа |
| `backend` | MTProto-бэкенд: per-user сессии (зашифрованы AES-256-GCM), вход и поиск |
| `mcp` | MCP-сервер (stdio), который OpenCode запускает как `tg-search` и который проксирует `search_tg` в backend |

Каждый пользователь логинится **своим** Telegram-аккаунтом (`/login <телефон>`,
код и 2FA приходят отдельными сообщениями), поэтому бот ищет и в приватных каналах,
к которым у пользователя есть доступ. Сессии хранятся зашифрованными на диске.

## Команды бота

- `/qrlogin` — вход через QR-код (основной способ; сканируете и подтверждаете на телефоне, при 2FA — вводите пароль).
- `/login <телефон>` — вход по номеру телефона (код + 2FA). С IP дата-центра Telegram часто блокирует этот способ — см. «Вход с VPS».
- `/import_session <json>` — импорт сессии, сгенерированной на чистом IP (см. «Вход с VPS»).
- `/set_channels @ch1 @ch2` — задать список каналов.
- `/add_channel @ch` / `/remove_channel @ch` — добавить/убрать канал.
- `/list_channels` — показать сохранённые каналы.
- обычное сообщение — вопрос, на который бот ищет ответ.

## Требования

- Go 1.26+
- OpenCode + DeepSeek API-ключ (модель задаётся в `OPENCODE_MODEL`)
- Telegram Bot API токен (`@BotFather`)
- MTProto `api_id`/`api_hash` с https://my.telegram.org/apps

## Сборка

```bash
go build ./...
# бинарники в ./bin (или любую целевую папку)
go build -o bin/bot ./cmd/bot
go build -o bin/backend ./cmd/backend
go build -o bin/mcp ./cmd/mcp
```

### Низкая память на VDS (2 ГБ)

Компиляция `github.com/gotd/td/tg` требует больше 2 ГБ RAM, поэтому на VDS с 2 ГБ
собирайте бинарники на другой машине и заливайте готовыми.

На своей машине (Mac/Linux с достаточной памятью):

```bash
./cross-compile.sh          # кладёт bot/backend/mcp в dist/ (linux/amd64)
```

На сервер нужно заливать **только эти три бинарника**. Весь остальной код —
`.opencode/`, `opencode.json`, `deploy/*.service`, `.env.example` — `deploy.sh`
сам подтянет через `git clone`:

```bash
scp dist/bot dist/backend dist/mcp user@IP:/tmp/
ssh user@IP
sudo mkdir -p /opt/candle-lurk-tg-bot/bin
sudo cp /tmp/bot /tmp/backend /tmp/mcp /opt/candle-lurk-tg-bot/bin/
sudo SKIP_BUILD=1 bash deploy.sh
```

Либо добавьте swap на VDS и собирайте на месте обычным `sudo bash deploy.sh`.

## Настройка

Скопируйте `.env.example` в env-файл (см. `deploy/*.service`, по умолчанию
`/etc/candle-lurk-tg-bot/env`) и заполните значения. Ключ шифрования сессий:

```bash
openssl rand -hex 32
```

OpenCode должен видеть `opencode.json` и `.opencode/` из рабочей директории
`opencode serve` — в юнитах это `/opt/candle-lurk-tg-bot`. Путь до MCP-бинаря в
`opencode.json` (`/opt/candle-lurk-tg-bot/bin/mcp`) поправьте под свой деплой.

`opencode serve` требует пароль: `OPENCODE_SERVER_PASSWORD` (в `EnvironmentFile`),
а DeepSeek-ключ передаётся стандартно (`/connect` или `DEEPSEEK_API_KEY`).

### Белый список пользователей

По умолчанию ботом может пользоваться любой. Чтобы ограничить доступ, задайте
переменную `ALLOWED_USERS` — список Telegram-логинов (без `@`) через запятую:

```
ALLOWED_USERS=alice,bob,carol
```

Если переменная задана, бот игнорирует сообщения от всех, кроме перечисленных
пользователей (сверка по `@username`).

## Деплой (systemd)

Проще всего — через `deploy.sh`: он ставит Git/Go/OpenCode, клонирует репозиторий,
собирает бинарники, создаёт env-файл и поднимает все три сервиса.

```bash
sudo bash deploy.sh
```

Первый запуск останавливается после создания env-файла — заполните его
(`/etc/candle-lurk-tg-bot/env`) и запустите скрипт ещё раз.

Переменные `deploy.sh`:

- `SKIP_BUILD=1` — не собирать бинарники, использовать уже лежащие в
  `/opt/candle-lurk-tg-bot/bin/`. Нужен на VDS с 2 ГБ, где сборка gotd падает по
  памяти (см. «Низкая память»).
- `GO_VERSION=1.26.2` — версия Go для установки.

`deploy.sh` по итогу делает то же, что вручную:

```bash
sudo cp deploy/*.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now backend.service opencode-serve.service bot.service
```

Все порты слушают только `127.0.0.1` — наружу ничего не открывается.

Сервисы работают от системного пользователя `lurk_ai_bot` в группе `users`;
каталог бота — `/opt/candle-lurk-tg-bot`, env-файл — `/etc/candle-lurk-tg-bot/env`.

## Как устроена обработка (OpenCode)

- Агент `answer` (`.opencode/agent/answer.md`) — роль и правила.
- Скиллы: `extract-keywords` (выделение поисковых запросов) и `answer-format`
  (оформление ответа со ссылками).
- Инструмент `search_tg` — MCP-сервер `tg-search`, объявленный в `opencode.json`.

Бот на каждый вопрос создаёт сессию, шлёт промпт с `user_id` и каналами, агент
вызывает `search_tg`, получает JSON с найденными сообщениями и возвращает ответ.

## Ограничения

- Telegram ищет по точному подстрочному совпадению, не по смыслу. Качество зависит
  от того, как агент выделит ключевые слова.
- Поиск работает только в каналах, доступных аккаунту пользователя.
- Вход по коду (`/login`) с IP дата-центра блокируется Telegram (`PHONE_CODE_EXPIRED`);
  основной путь — QR-вход (`/qrlogin`) или импорт сессии (`/import_session`).

## Вход с VPS

Telegram аннулирует коды входа (`PHONE_CODE_EXPIRED`) и быстро гасит QR-токены,
когда логин идёт с IP дата-центра. Два рабочих способа:

**1. QR-вход (основной, для обычных пользователей).** `/qrlogin` — бот присылает
картинку QR-кода. Отсканируйте её на телефоне (Telegram → Настройки → Устройства →
Подключить устройство), подтвердите вход, при включённой 2FA введите пароль.

**2. Импорт сессии (запасной).** Сгенерируйте сессию на машине с чистым IP:

```bash
# на домашнем ПК/ноутбуке, из корня репозитория:
go run ./cmd/login-gen <api_id> <api_hash> <телефон>
# программа попросит код (и 2FA) и выведет JSON сессии на stdout
```

Скопируйте вывод и отправьте боту:

```
/import_session <json>
```

## Безопасность

- MTProto-сессии шифруются AES-256-GCM (`SESSION_KEY`) и хранятся с правами `0600`.
- `SESSION_KEY`, токены и пароли не коммитятся (см. `.gitignore`).
- Не открывайте порты бэкенда и opencode наружу; всё — на loopback.
