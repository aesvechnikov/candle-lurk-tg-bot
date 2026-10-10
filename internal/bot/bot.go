package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mymmrac/telego"

	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/backend"
	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/opencode"
	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/store"
)

const (
	agentName = "answer"
	msgLimit  = 4000 // Telegram text message limit is 4096; keep a safety margin.
)

// Bot wires the Telegram frontend to the MTProto backend and OpenCode.
type Bot struct {
	tele    *telego.Bot
	store   *store.Store
	backend *backend.Client
	oc      *opencode.Client
	model   string

	mu     sync.Mutex
	logins map[int64]loginState
}

type loginState struct {
	step  string // "code" or "password"
	phone string
}

func New(
	token string,
	st *store.Store,
	b *backend.Client,
	oc *opencode.Client,
	model string,
) (*Bot, error) {
	tg, err := telego.NewBot(token)
	if err != nil {
		return nil, err
	}
	return &Bot{
		tele:    tg,
		store:   st,
		backend: b,
		oc:      oc,
		model:   model,
		logins:  map[int64]loginState{},
	}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	updates, err := b.tele.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		return err
	}
	for upd := range updates {
		b.handleUpdate(ctx, upd)
	}
	return nil
}

func (b *Bot) handleUpdate(ctx context.Context, upd telego.Update) {
	msg := upd.Message
	if msg == nil {
		return
	}
	chatID := msg.Chat.ID
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	if strings.HasPrefix(text, "/") {
		b.handleCommand(ctx, chatID, text)
		return
	}

	// Plain text: either a login step or a question.
	if b.inLogin(chatID) {
		b.handleLoginInput(ctx, chatID, text)
		return
	}
	b.handleQuestion(ctx, chatID, text)
}

func (b *Bot) handleCommand(ctx context.Context, chatID int64, text string) {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "/start", "/help":
		b.reply(ctx, chatID, helpText)
	case "/login":
		if len(args) == 0 {
			b.reply(ctx, chatID, "Укажите номер телефона в международном формате:\n/login +79991234567")
			return
		}
		b.startLogin(ctx, chatID, args[0])
	case "/set_channels":
		b.setChannels(ctx, chatID, args)
	case "/add_channel":
		b.addChannels(ctx, chatID, args)
	case "/remove_channel":
		b.removeChannels(ctx, chatID, args)
	case "/list_channels":
		b.listChannels(ctx, chatID)
	default:
		b.reply(ctx, chatID, "Неизвестная команда. /help — список команд.")
	}
}

func (b *Bot) startLogin(ctx context.Context, chatID int64, phone string) {
	status, err := b.backend.LoginStart(ctx, chatID, phone)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось начать вход: "+err.Error())
		return
	}
	switch status {
	case "code":
		b.setLogin(chatID, loginState{step: "code", phone: phone})
		b.reply(ctx, chatID, "Введите код, который Telegram прислал вам в личку:")
	case "done":
		_ = b.store.UpsertUser(chatID, phone)
		b.reply(ctx, chatID, "Вы уже авторизованы.")
	}
}

func (b *Bot) handleLoginInput(ctx context.Context, chatID int64, text string) {
	state := b.getLogin(chatID)
	switch state.step {
	case "code":
		status, err := b.backend.LoginCode(ctx, chatID, text)
		if err != nil {
			b.clearLogin(chatID)
			b.reply(ctx, chatID, "Ошибка входа: "+err.Error())
			return
		}
		switch status {
		case "password":
			b.setLogin(chatID, loginState{step: "password", phone: state.phone})
			b.reply(ctx, chatID, "Введите пароль двухфакторной аутентификации:")
		case "done":
			b.clearLogin(chatID)
			_ = b.store.UpsertUser(chatID, state.phone)
			b.reply(ctx, chatID, "Готово, вход выполнен.")
		}
	case "password":
		status, err := b.backend.LoginPassword(ctx, chatID, text)
		if err != nil {
			b.clearLogin(chatID)
			b.reply(ctx, chatID, "Ошибка входа: "+err.Error())
			return
		}
		if status == "done" {
			b.clearLogin(chatID)
			_ = b.store.UpsertUser(chatID, state.phone)
			b.reply(ctx, chatID, "Готово, вход выполнен.")
		}
	}
}

func (b *Bot) setChannels(ctx context.Context, chatID int64, channels []string) {
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Укажите каналы: /set_channels @channel1 @channel2")
		return
	}
	if err := b.store.SetChannels(chatID, channels); err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	b.listChannels(ctx, chatID)
}

func (b *Bot) addChannels(ctx context.Context, chatID int64, channels []string) {
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Укажите каналы: /add_channel @channel1 @channel2")
		return
	}
	if err := b.store.AddChannels(chatID, channels); err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	b.listChannels(ctx, chatID)
}

func (b *Bot) removeChannels(ctx context.Context, chatID int64, channels []string) {
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Укажите каналы: /remove_channel @channel1")
		return
	}
	for _, c := range channels {
		_ = b.store.RemoveChannel(chatID, c)
	}
	b.listChannels(ctx, chatID)
}

func (b *Bot) listChannels(ctx context.Context, chatID int64) {
	channels, err := b.store.ListChannels(chatID)
	if err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Каналы не заданы. Добавьте: /set_channels @channel1 @channel2")
		return
	}
	b.reply(ctx, chatID, "Каналы:\n"+strings.Join(channels, "\n"))
}

func (b *Bot) handleQuestion(ctx context.Context, chatID int64, question string) {
	channels, err := b.store.ListChannels(chatID)
	if err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Сначала добавьте каналы: /set_channels @channel1 @channel2")
		return
	}

	b.reply(ctx, chatID, "Ищу по каналам…")

	prompt := buildPrompt(chatID, channels, question)
	answer, err := b.oc.Chat(ctx, agentName, b.model, prompt)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось получить ответ: "+err.Error())
		return
	}
	if answer == "" {
		b.reply(ctx, chatID, "Ничего не найдено.")
		return
	}
	for _, chunk := range splitMessage(answer, msgLimit) {
		b.reply(ctx, chatID, chunk)
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	_, _ = b.tele.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   text,
	})
}

func (b *Bot) setLogin(chatID int64, s loginState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logins[chatID] = s
}

func (b *Bot) getLogin(chatID int64) loginState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logins[chatID]
}

func (b *Bot) inLogin(chatID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.logins[chatID]
	return ok
}

func (b *Bot) clearLogin(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.logins, chatID)
}

func buildPrompt(chatID int64, channels []string, question string) string {
	return fmt.Sprintf(`Ты — помощник, который отвечает на вопросы, находя информацию в Telegram-каналах пользователя.

Твой user_id: %d
Каналы, в которых нужно искать: %s

Вопрос пользователя: %s

Действуй по шагам:
1. Выдели из вопроса 1–4 конкретных поисковых запроса (отдельные слова или короткие фразы).
2. Для каждого запроса вызови инструмент search_tg, передав user_id=%d, channels и query.
3. Собери короткий ответ на русском, по существу, с ссылками на источники (поле link в результатах).

Требования к ответу: кратко, на русском, без выдумок; если результатов нет — так и скажи. Ссылки приводи только те, что реально есть в результатах.`,
		chatID, strings.Join(channels, ", "), question, chatID)
}

func splitMessage(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var out []string
	for len(text) > limit {
		cut := limit
		if idx := strings.LastIndexByte(text[:limit], '\n'); idx > limit/2 {
			cut = idx
		}
		out = append(out, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n")
	}
	if text != "" {
		out = append(out, text)
	}
	return out
}

const helpText = `Бот ищет ответы по вашим Telegram-каналам.

Команды:
/login <телефон> — вход в ваш аккаунт Telegram (код и 2FA придут отдельными сообщениями).
/set_channels @ch1 @ch2 — задать список каналов.
/add_channel @ch1 — добавить канал.
/remove_channel @ch1 — убрать канал.
/list_channels — показать каналы.

После этого просто напишите вопрос обычным текстом — бот найдёт ответ и пришлёт ссылки на источники.`
