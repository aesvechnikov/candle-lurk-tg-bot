package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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
	allowed map[string]bool

	mu     sync.Mutex
	logins map[int64]bool // chatID -> awaiting the QR 2FA password
}

func New(
	token string,
	st *store.Store,
	b *backend.Client,
	oc *opencode.Client,
	model string,
	allowed []string,
) (*Bot, error) {
	tg, err := telego.NewBot(token)
	if err != nil {
		return nil, err
	}
	allowedMap := make(map[string]bool, len(allowed))
	for _, u := range allowed {
		allowedMap[strings.ToLower(u)] = true
	}
	return &Bot{
		tele:    tg,
		store:   st,
		backend: b,
		oc:      oc,
		model:   model,
		allowed: allowedMap,
		logins:  map[int64]bool{},
	}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	if err := b.registerCommands(ctx); err != nil {
		return err
	}
	updates, err := b.tele.UpdatesViaLongPolling(ctx, nil)
	if err != nil {
		return err
	}
	for upd := range updates {
		b.handleUpdate(ctx, upd)
	}
	return nil
}

// registerCommands publishes the command list so Telegram shows suggestions
// when the user types "/".
func (b *Bot) registerCommands(ctx context.Context) error {
	commands := []telego.BotCommand{
		{Command: "qrlogin", Description: "вход через QR-код"},
		{Command: "add_channel", Description: "добавить канал"},
		{Command: "remove_channel", Description: "убрать канал"},
		{Command: "list_channels", Description: "показать каналы"},
		{Command: "help", Description: "справка"},
	}
	return b.tele.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: commands})
}

func (b *Bot) handleUpdate(ctx context.Context, upd telego.Update) {
	msg := upd.Message
	if msg == nil {
		return
	}
	if !b.isAllowed(msg.From) {
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

	// Plain text: either the 2FA password or a question.
	if b.awaitingPassword(chatID) {
		b.handleQRPassword(ctx, chatID, text, msg.MessageID)
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
	case "/qrlogin":
		b.startQRLogin(ctx, chatID)
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

func (b *Bot) startQRLogin(ctx context.Context, chatID int64) {
	url, png, err := b.backend.QRLoginStart(ctx, chatID)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось начать QR-вход: "+friendlyLoginError(err))
		return
	}
	b.sendQR(ctx, chatID, png, url)

	const maxRefreshes = 3
	refreshes := 0
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		status, newURL, newPNG, err := b.backend.QRLoginPoll(ctx, chatID)
		if err != nil {
			b.reply(ctx, chatID, friendlyLoginError(err))
			return
		}
		switch status {
		case "done":
			b.reply(ctx, chatID, "Готово, вход выполнен.")
			return
		case "password":
			b.setAwaitingPassword(chatID)
			b.reply(ctx, chatID, "Введите пароль двухфакторной аутентификации:")
			return
		case "refresh":
			refreshes++
			if refreshes > maxRefreshes {
				b.reply(ctx, chatID, "QR-код истекает слишком часто. Попробуйте /qrlogin ещё раз.")
				return
			}
			b.sendQR(ctx, chatID, newPNG, newURL)
		}
	}
	b.reply(ctx, chatID, "Время входа истекло. Попробуйте /qrlogin ещё раз.")
}

// friendlyLoginError converts gotd's raw timeout errors into a readable message.
func friendlyLoginError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timed out") {
		return "Время входа истекло. Попробуйте ещё раз."
	}
	return "Ошибка входа: " + msg
}

// sendQR sends the login QR code as a photo, with the tappable URL as caption.
func (b *Bot) sendQR(ctx context.Context, chatID int64, pngBase64, url string) {
	caption := "Отсканируйте QR-код приложением Telegram или откройте ссылку и подтвердите вход:\n" + url

	png, err := base64.StdEncoding.DecodeString(pngBase64)
	if err != nil || len(png) == 0 {
		b.reply(ctx, chatID, caption)
		return
	}

	_, _ = b.tele.SendPhoto(ctx, &telego.SendPhotoParams{
		ChatID:  telego.ChatID{ID: chatID},
		Photo:   telego.InputFile{File: namedBytes{Reader: bytes.NewReader(png), name: "qr.png"}},
		Caption: caption,
	})
}

// namedBytes adapts an in-memory byte slice to telego's NamedReader for uploads.
type namedBytes struct {
	*bytes.Reader
	name string
}

func (n namedBytes) Name() string { return n.name }

// handleQRPassword submits the QR 2FA password and deletes the user's message
// so the password does not linger in the chat history.
func (b *Bot) handleQRPassword(ctx context.Context, chatID int64, password string, messageID int) {
	b.clearAwaitingPassword(chatID)
	b.deleteMessage(ctx, chatID, messageID)

	status, err := b.backend.QRLoginPassword(ctx, chatID, password)
	if err != nil {
		b.reply(ctx, chatID, friendlyLoginError(err))
		return
	}
	if status == "done" {
		b.reply(ctx, chatID, "Готово, вход выполнен.")
	}
}

func (b *Bot) deleteMessage(ctx context.Context, chatID int64, messageID int) {
	_ = b.tele.DeleteMessage(ctx, &telego.DeleteMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		MessageID: messageID,
	})
}

func (b *Bot) addChannels(ctx context.Context, chatID int64, names []string) {
	if len(names) == 0 {
		b.reply(ctx, chatID, "Укажите каналы: /add_channel @channel1 @channel2")
		return
	}
	var channels []store.Channel
	for _, name := range names {
		c := store.Channel{Name: name}
		if title, about, err := b.backend.ChannelInfo(ctx, chatID, name); err == nil {
			c.Title = title
			c.About = about
		}
		channels = append(channels, c)
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
		b.reply(ctx, chatID, "Каналы не заданы. Добавьте: /add_channel @channel1")
		return
	}
	names := make([]string, 0, len(channels))
	for _, c := range channels {
		names = append(names, c.Name)
	}
	b.reply(ctx, chatID, "Каналы:\n"+strings.Join(names, "\n"))
}

func (b *Bot) handleQuestion(ctx context.Context, chatID int64, question string) {
	channels, err := b.store.ListChannels(chatID)
	if err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Сначала добавьте каналы: /add_channel @channel1")
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
		b.replyMarkdown(ctx, chatID, chunk)
	}
}

func (b *Bot) reply(ctx context.Context, chatID int64, text string) {
	_, _ = b.tele.SendMessage(ctx, &telego.SendMessageParams{
		ChatID: telego.ChatID{ID: chatID},
		Text:   text,
	})
}

// replyMarkdown converts the agent's minimal Markdown (**bold**, [text](url))
// to Telegram HTML and sends it, so formatting renders instead of showing
// literal asterisks/brackets.
func (b *Bot) replyMarkdown(ctx context.Context, chatID int64, text string) {
	_, _ = b.tele.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:    telego.ChatID{ID: chatID},
		Text:      markdownToHTML(text),
		ParseMode: "HTML",
	})
}

var (
	reBold = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// markdownToHTML converts the subset of Markdown the agent is allowed to use
// into Telegram HTML: **bold** -> <b>, [text](url) -> <a>. Everything else is
// escaped so it is shown verbatim.
func markdownToHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = reBold.ReplaceAllString(s, "<b>$1</b>")
	s = reLink.ReplaceAllString(s, `<a href="$2">$1</a>`)
	return s
}

// isAllowed reports whether a user may use the bot. An empty whitelist allows
// everyone; otherwise the user's Telegram username must be listed.
func (b *Bot) isAllowed(from *telego.User) bool {
	if len(b.allowed) == 0 {
		return true
	}
	if from == nil || from.Username == "" {
		return false
	}
	return b.allowed[strings.ToLower(from.Username)]
}

func (b *Bot) setAwaitingPassword(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logins[chatID] = true
}

func (b *Bot) awaitingPassword(chatID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.logins[chatID]
}

func (b *Bot) clearAwaitingPassword(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.logins, chatID)
}

func buildPrompt(chatID int64, channels []store.Channel, question string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ты — помощник, который отвечает на вопросы, находя информацию в Telegram-каналах пользователя.\n\n")
	fmt.Fprintf(&b, "Твой user_id: %d\n", chatID)
	b.WriteString("Каналы (с описаниями):\n")
	for _, c := range channels {
		desc := strings.TrimSpace(c.About)
		if desc == "" {
			desc = strings.TrimSpace(c.Title)
		}
		if desc == "" {
			desc = "(без описания)"
		}
		fmt.Fprintf(&b, "- %s — %s\n", c.Name, desc)
	}
	fmt.Fprintf(&b, "\nВопрос пользователя: %s\n\n", question)
	b.WriteString("Действуй по шагам:\n")
	b.WriteString("1. Выдели из вопроса 1–2 конкретных поисковых запроса.\n")
	b.WriteString("2. Выбери 1–4 канала, которые с наибольшей вероятностью содержат ответ (см. скилл select-channels), и ищи только в них.\n")
	fmt.Fprintf(&b, "3. Для каждого запроса вызови инструмент search_tg, передав user_id=%d, channels=[выбранные каналы] и query=запрос.\n", chatID)
	b.WriteString("4. Собери короткий ответ на русском со ссылками на источники (поле link в результатах).\n\n")
	b.WriteString("Требования к ответу: кратко, на русском, без выдумок; если результатов нет — так и скажи. Ссылки приводи только те, что реально есть в результатах.\n")
	return b.String()
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
/qrlogin — вход через QR-код (сканируете и подтверждаете на телефоне).
/add_channel @ch1 — добавить канал.
/remove_channel @ch1 — убрать канал.
/list_channels — показать каналы.

После этого просто напишите вопрос обычным текстом — бот найдёт ответ и пришлёт ссылки на источники.`
