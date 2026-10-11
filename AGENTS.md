# Правила проекта (OpenCode)

Этот репозиторий — бот `tg-lurk-ai-bot`: отвечает на вопросы, ища информацию
в Telegram-каналах пользователя.

## Поведение агента `answer`

- Отвечай на русском, кратко и по существу.
- Для поиска используй только инструмент `search_tg` (MCP-сервер `tg-search`).
- Выбирай релевантные каналы по описаниям (скилл `select-channels`) и передавай
  в `search_tg` только их, а не весь список.
- Всегда приводи ссылки на источники из поля `link` результата; не выдумывай ссылки.
- Не выдумывай факты; если результатов нет — скажи об этом честно.

## Работа с кодом

- Go-проект, стек: gotd (MTProto), telego (Telegram Bot API), mcp-go, SQLite, vosk (STT).
- Бинарники `tg-lurk-ai-bot`, `tg-lurk-ai-bot-backend`, `tg-lurk-ai-bot-mcp` + `tg-lurk-ai-bot-stt` (STT-сервис, собирается отдельно с `-tags vosk` и cgo)
  и headless `opencode serve` с агентом `answer`.
- `stt` компилируется только с `-tags vosk` (нужны libvosk + cgo); обычный `go build ./...` его пропускает.
- Сборка: `go build ./...`. Линк/проверка: `go vet ./...`. Тесты: `go test ./...`.
- Не коммить `.env`, `data/`, `bin/`, `dist/`.

## Синхронизация документации

- При изменении потоков вызовов обновляй диаграмму: исходник `docs/architecture.puml`,
  затем перегенерируй картинку (`plantuml -tpng docs/architecture.puml`) и при
  изменении `architecture.puml` переименуй PNG (GitHub агрессивно кэширует картинки).
- При изменении схемы БД (`internal/store/store.go`) обновляй раздел «Схема БД» в README.
