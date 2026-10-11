package bot

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"

	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/backend"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/opencode"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/store"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/stt"
)

const (
	agentName            = "answer"
	describeChannelAgent = "describe-channel"
	msgLimit             = 4000 // Telegram text message limit is 4096; keep a safety margin.
)

// Bot wires the Telegram frontend to the MTProto backend and OpenCode.
type Bot struct {
	tele    *telego.Bot
	token   string
	store   *store.Store
	backend *backend.Client
	oc      *opencode.Client
	model   string
	allowed map[string]bool
	stt     *stt.Client
	sttOn   bool

	mu            sync.Mutex
	logins        map[int64]bool // chatID -> awaiting the QR 2FA password
	pendingAdd    map[int64]bool // chatID -> awaiting channel names for /add_channels
	pendingRemove map[int64]bool // chatID -> awaiting channel names for /remove_channels
}

func New(
	token string,
	st *store.Store,
	b *backend.Client,
	oc *opencode.Client,
	model string,
	allowed []string,
	sttClient *stt.Client,
	sttEnabled bool,
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
		tele:          tg,
		token:         token,
		store:         st,
		backend:       b,
		oc:            oc,
		model:         model,
		allowed:       allowedMap,
		stt:           sttClient,
		sttOn:         sttEnabled,
		logins:        map[int64]bool{},
		pendingAdd:    map[int64]bool{},
		pendingRemove: map[int64]bool{},
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
// guestCommands is the default command list, shown before login. Channel
// management commands are added later via a per-chat scope once the user is
// logged in.
var guestCommands = []telego.BotCommand{
	{Command: "qrlogin", Description: "вход через QR-код"},
	{Command: "cancel", Description: "отменить текущее действие"},
	{Command: "help", Description: "справка"},
}

// fullCommands is the command list for a logged-in chat.
var fullCommands = []telego.BotCommand{
	{Command: "qrlogin", Description: "вход через QR-код"},
	{Command: "add_channels", Description: "добавить каналы"},
	{Command: "remove_channels", Description: "убрать каналы"},
	{Command: "list_channels", Description: "показать каналы"},
	{Command: "delete_me", Description: "удалить аккаунт и все данные"},
	{Command: "cancel", Description: "отменить текущее действие"},
	{Command: "help", Description: "справка"},
}

func (b *Bot) registerCommands(ctx context.Context) error {
	return b.tele.SetMyCommands(ctx, &telego.SetMyCommandsParams{Commands: guestCommands})
}

func chatScope(chatID int64) *telego.BotCommandScopeChat {
	return &telego.BotCommandScopeChat{Type: telego.ScopeTypeChat, ChatID: telego.ChatID{ID: chatID}}
}

// setFullCommands switches a chat to the logged-in command list.
func (b *Bot) setFullCommands(ctx context.Context, chatID int64) {
	_ = b.tele.SetMyCommands(ctx, &telego.SetMyCommandsParams{
		Commands: fullCommands,
		Scope:    chatScope(chatID),
	})
}

// resetCommands removes the per-chat override so the guest list applies again.
func (b *Bot) resetCommands(ctx context.Context, chatID int64) {
	_ = b.tele.DeleteMyCommands(ctx, &telego.DeleteMyCommandsParams{Scope: chatScope(chatID)})
}

func (b *Bot) handleUpdate(ctx context.Context, upd telego.Update) {
	if upd.CallbackQuery != nil {
		b.handleCallback(ctx, upd.CallbackQuery)
		return
	}
	msg := upd.Message
	if msg == nil {
		return
	}
	if !b.isAllowed(msg.From) {
		return
	}
	chatID := msg.Chat.ID

	if msg.Voice != nil {
		if b.sttOn {
			b.handleVoice(ctx, chatID, msg.Voice)
		} else {
			b.reply(ctx, chatID, "Голосовые сообщения не поддерживаются.")
		}
		return
	}

	// A forwarded message while adding channels: add the source channel.
	if b.awaitingAdd(chatID) && msg.ForwardOrigin != nil {
		b.handleAddForward(ctx, chatID, msg)
		return
	}

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	if strings.HasPrefix(text, "/") {
		b.handleCommand(ctx, chatID, text)
		return
	}

	// Plain text: the 2FA password, channel names, or a question.
	if b.awaitingPassword(chatID) {
		b.handleQRPassword(ctx, chatID, text, msg.MessageID)
		return
	}
	if b.awaitingAdd(chatID) {
		b.handleAddChannelsInput(ctx, chatID, text)
		return
	}
	if b.awaitingRemove(chatID) {
		b.handleRemoveChannelsInput(ctx, chatID, text)
		return
	}
	b.handleQuestion(ctx, chatID, text)
}

func (b *Bot) handleCommand(ctx context.Context, chatID int64, text string) {
	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])

	switch cmd {
	case "/start":
		b.welcome(ctx, chatID)
	case "/help":
		b.reply(ctx, chatID, b.helpTextFor(ctx, chatID))
	case "/qrlogin":
		b.startQRLogin(ctx, chatID)
	case "/cancel":
		b.cancel(ctx, chatID)
	case "/add_channels":
		if !b.requireAuth(ctx, chatID) {
			return
		}
		b.setAwaitingAdd(chatID)
		b.reply(ctx, chatID, "Отправьте имена каналов (@channel1 @channel2) или ссылку t.me/канал.")
	case "/remove_channels":
		if !b.requireAuth(ctx, chatID) {
			return
		}
		b.setAwaitingRemove(chatID)
		b.reply(ctx, chatID, b.removePrompt(chatID))
	case "/list_channels":
		if !b.requireAuth(ctx, chatID) {
			return
		}
		b.listChannels(ctx, chatID)
	case "/delete_me":
		b.deleteMe(ctx, chatID)
	default:
		b.reply(ctx, chatID, "Неизвестная команда. /help — список команд.")
	}
}

// handleCallback answers an inline keyboard button press.
func (b *Bot) handleCallback(ctx context.Context, cq *telego.CallbackQuery) {
	_ = b.tele.AnswerCallbackQuery(ctx, &telego.AnswerCallbackQueryParams{CallbackQueryID: cq.ID})
	chatID := cq.From.ID

	switch cq.Data {
	case "qrlogin":
		b.startQRLogin(ctx, chatID)
	case "help":
		b.welcome(ctx, chatID)
	}
}

// welcome sends the greeting with quick-action buttons.
func (b *Bot) welcome(ctx context.Context, chatID int64) {
	_, _ = b.tele.SendMessage(ctx, &telego.SendMessageParams{
		ChatID:      telego.ChatID{ID: chatID},
		Text:        b.helpTextFor(ctx, chatID),
		ReplyMarkup: welcomeMarkup,
	})
}

// helpTextFor returns the full help after login and the guest help before.
func (b *Bot) helpTextFor(ctx context.Context, chatID int64) string {
	ok, err := b.backend.Authorized(ctx, chatID)
	if err == nil && ok {
		return helpText
	}
	return guestHelpText
}

// cancel aborts whatever interactive action is in progress: the QR login
// (2FA step) or waiting for channel names.
func (b *Bot) cancel(ctx context.Context, chatID int64) {
	if !b.awaitingPassword(chatID) && !b.awaitingAdd(chatID) && !b.awaitingRemove(chatID) {
		b.reply(ctx, chatID, "Нет активного действия.")
		return
	}
	b.clearAwaitingPassword(chatID)
	b.clearAwaitingAdd(chatID)
	b.clearAwaitingRemove(chatID)
	b.reply(ctx, chatID, "Действие отменено.")
}

// requireAuth replies and returns false when the user has no active MTProto
// session, gating channel management and questions behind a login.
func (b *Bot) requireAuth(ctx context.Context, chatID int64) bool {
	ok, err := b.backend.Authorized(ctx, chatID)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось проверить вход: "+err.Error())
		return false
	}
	if !ok {
		b.reply(ctx, chatID, "Сначала войдите через /qrlogin.")
		return false
	}
	return true
}

func (b *Bot) startQRLogin(ctx context.Context, chatID int64) {
	_, png, err := b.backend.QRLoginStart(ctx, chatID)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось начать QR-вход: "+friendlyLoginError(err))
		return
	}
	b.sendQR(ctx, chatID, png)

	const maxRefreshes = 3
	refreshes := 0
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		status, _, newPNG, err := b.backend.QRLoginPoll(ctx, chatID)
		if err != nil {
			b.reply(ctx, chatID, friendlyLoginError(err))
			return
		}
		switch status {
		case "done":
			b.setFullCommands(ctx, chatID)
			b.reply(ctx, chatID, "Готово, вход выполнен.")
			return
		case "password":
			b.setAwaitingPassword(chatID)
			b.reply(ctx, chatID, "Введите пароль двухфакторной аутентификации.\nБот не хранит пароль, а сообщение с ним будет удалено сразу после обработки.")
			return
		case "refresh":
			refreshes++
			if refreshes > maxRefreshes {
				b.reply(ctx, chatID, "QR-код истекает слишком часто. Попробуйте ещё раз.")
				return
			}
			b.sendQR(ctx, chatID, newPNG)
		}
	}
	b.reply(ctx, chatID, "Время входа истекло. Попробуйте ещё раз.")
}

// friendlyLoginError converts gotd's raw timeout errors into a readable message.
func friendlyLoginError(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "deadline exceeded") || strings.Contains(msg, "timed out") {
		return "Время входа истекло. Попробуйте ещё раз."
	}
	return "Ошибка входа: " + msg
}

// sendQR sends the login QR code as a photo. The caption intentionally omits
// the tappable login URL: it carries the same one-time token as the QR, and
// tapping it (often repeatedly) consumes the token and breaks the login.
func (b *Bot) sendQR(ctx context.Context, chatID int64, pngBase64 string) {
	caption := "Отсканируйте QR-код приложением Telegram:\nНастройки → Устройства → Подключить устройство.\n\n🔒 Бот не сохраняет пароли и персональные данные."

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
		b.setFullCommands(ctx, chatID)
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
		b.reply(ctx, chatID, "Не получил имена каналов.")
		return
	}
	var channels []store.Channel
	for _, name := range names {
		username := channelUsername(name)
		if username == "" {
			continue
		}
		c := store.Channel{Name: username}
		if title, about, err := b.backend.ChannelInfo(ctx, chatID, username); err == nil {
			c.Title = title
			c.About = b.summarizeChannel(ctx, title, about)
		}
		channels = append(channels, c)
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Не удалось распознать ни один канал.")
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
		b.reply(ctx, chatID, "Не получил имена каналов.")
		return
	}
	for _, c := range channels {
		if username := channelUsername(c); username != "" {
			_ = b.store.RemoveChannel(chatID, username)
		}
	}
	b.listChannels(ctx, chatID)
}

// summarizeChannel asks the model for a one-sentence description of the
// channel's content, without links or contact info. Falls back to the raw
// description if the model is unavailable.
func (b *Bot) summarizeChannel(ctx context.Context, title, about string) string {
	title = strings.TrimSpace(title)
	about = strings.TrimSpace(about)
	if title == "" && about == "" {
		return ""
	}
	prompt := fmt.Sprintf("Опиши кратко, о чём канал «%s».\nОписание канала: %s", title, about)
	summary, err := b.oc.Chat(ctx, describeChannelAgent, b.model, prompt)
	if err != nil || strings.TrimSpace(summary) == "" {
		return about
	}
	return strings.TrimSpace(summary)
}

// channelUsername normalizes a channel reference to a bare username:
// "@name", "name", "https://t.me/name", "t.me/s/name" -> "name".
// Returns "" for unparsable input (e.g. a private invite link t.me/+hash).
func channelUsername(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "@")
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "http://")
	for _, host := range []string{"t.me/s/", "telegram.me/s/", "t.me/", "telegram.me/"} {
		if strings.HasPrefix(raw, host) {
			raw = strings.TrimPrefix(raw, host)
			break
		}
	}
	if i := strings.IndexAny(raw, "/?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "+") {
		return ""
	}
	return raw
}

func (b *Bot) listChannels(ctx context.Context, chatID int64) {
	channels, err := b.store.ListChannels(chatID)
	if err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Каналы не заданы. Добавьте их командой /add_channels")
		return
	}
	lines := make([]string, 0, len(channels))
	for _, c := range channels {
		lines = append(lines, formatChannel(c))
	}
	b.reply(ctx, chatID, "Каналы:\n"+strings.Join(lines, "\n\n"))
}

// deleteMe removes the user's session and saved channels, effectively
// disconnecting the bot from the user's account.
func (b *Bot) deleteMe(ctx context.Context, chatID int64) {
	b.clearAwaitingPassword(chatID)
	b.clearAwaitingAdd(chatID)
	b.clearAwaitingRemove(chatID)

	if err := b.store.DeleteUser(chatID); err != nil {
		b.reply(ctx, chatID, "Не удалось удалить данные: "+err.Error())
		return
	}
	if err := b.backend.DeleteUser(ctx, chatID); err != nil {
		b.reply(ctx, chatID, "Каналы удалены, но не удалось удалить сессию: "+err.Error())
		return
	}
	b.resetCommands(ctx, chatID)
	b.reply(ctx, chatID, "Готово. Вход и все данные удалены. Напишите /start, чтобы начать заново.")
}

func (b *Bot) handleQuestion(ctx context.Context, chatID int64, question string) {
	if !b.requireAuth(ctx, chatID) {
		return
	}
	channels, err := b.store.ListChannels(chatID)
	if err != nil {
		b.reply(ctx, chatID, "Ошибка: "+err.Error())
		return
	}
	if len(channels) == 0 {
		b.reply(ctx, chatID, "Сначала добавьте каналы командой /add_channels")
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

// handleVoice transcribes a voice message and feeds the recognized text into
// the normal question pipeline.
func (b *Bot) handleVoice(ctx context.Context, chatID int64, voice *telego.Voice) {
	b.reply(ctx, chatID, "Распознаю голосовое…")

	pcm, err := b.voiceToPCM(ctx, voice.FileID)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось обработать голосовое: "+err.Error())
		return
	}
	text, err := b.stt.Transcribe(ctx, pcm)
	if err != nil {
		b.reply(ctx, chatID, "Не удалось распознать голосовое: "+err.Error())
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		b.reply(ctx, chatID, "Не удалось распознать речь.")
		return
	}
	b.reply(ctx, chatID, "Распознано: "+text)
	b.handleQuestion(ctx, chatID, text)
}

// voiceToPCM downloads a voice note and converts it to 16 kHz mono s16le PCM.
func (b *Bot) voiceToPCM(ctx context.Context, fileID string) ([]byte, error) {
	file, err := b.tele.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, err
	}
	ogg, err := b.downloadFile(ctx, file.FilePath)
	if err != nil {
		return nil, err
	}
	return convertToPCM(ctx, ogg)
}

func (b *Bot) downloadFile(ctx context.Context, filePath string) ([]byte, error) {
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("download status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// convertToPCM converts OGG/Opus audio to raw 16 kHz mono s16le PCM via ffmpeg.
func convertToPCM(ctx context.Context, ogg []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ac", "1",
		"-ar", "16000",
		"pipe:1",
	)
	cmd.Stdin = bytes.NewReader(ogg)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %w: %s", err, errBuf.String())
	}
	return out.Bytes(), nil
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

func (b *Bot) setAwaitingAdd(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingAdd[chatID] = true
}

func (b *Bot) awaitingAdd(chatID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingAdd[chatID]
}

func (b *Bot) clearAwaitingAdd(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pendingAdd, chatID)
}

// handleAddChannelsInput treats the next plain message as channel names.
func (b *Bot) handleAddChannelsInput(ctx context.Context, chatID int64, text string) {
	b.clearAwaitingAdd(chatID)
	names := strings.Fields(text)
	if len(names) == 0 {
		b.reply(ctx, chatID, "Не получил имена каналов.")
		return
	}
	b.addChannels(ctx, chatID, names)
}

// handleAddForward adds the channel a forwarded message came from. The add
// state is kept so several channels can be added by forwarding in a row.
func (b *Bot) handleAddForward(ctx context.Context, chatID int64, msg *telego.Message) {
	username := forwardOriginUsername(msg)
	if username == "" {
		b.reply(ctx, chatID, "Не удалось определить канал. Перешлите сообщение из публичного канала (у приватного нет логина).")
		return
	}
	b.addChannels(ctx, chatID, []string{"@" + username})
	b.reply(ctx, chatID, "Можно переслать ещё одно сообщение из канала или написать /cancel.")
}

// forwardOriginUsername returns the username of the channel a message was
// forwarded from, or "" for a user forward or a channel without a username.
func forwardOriginUsername(msg *telego.Message) string {
	switch origin := msg.ForwardOrigin.(type) {
	case *telego.MessageOriginChannel:
		return origin.Chat.Username
	case *telego.MessageOriginChat:
		return origin.SenderChat.Username
	}
	return ""
}

func (b *Bot) setAwaitingRemove(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pendingRemove[chatID] = true
}

func (b *Bot) awaitingRemove(chatID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pendingRemove[chatID]
}

func (b *Bot) clearAwaitingRemove(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pendingRemove, chatID)
}

// handleRemoveChannelsInput treats the next plain message as channel names to remove.
func (b *Bot) handleRemoveChannelsInput(ctx context.Context, chatID int64, text string) {
	b.clearAwaitingRemove(chatID)
	names := strings.Fields(text)
	if len(names) == 0 {
		b.reply(ctx, chatID, "Не получил имена каналов.")
		return
	}
	b.removeChannels(ctx, chatID, names)
}

// removePrompt lists the current channels so the user knows what they can remove.
func (b *Bot) removePrompt(chatID int64) string {
	channels, err := b.store.ListChannels(chatID)
	if err != nil || len(channels) == 0 {
		return "Каналы не заданы."
	}
	lines := make([]string, 0, len(channels))
	for _, c := range channels {
		lines = append(lines, formatChannel(c))
	}
	return "Ваши каналы:\n" + strings.Join(lines, "\n\n") +
		"\n\nОтправьте имена каналов, которые нужно убрать (через пробел):"
}

// formatChannel renders a channel as a compact one-liner for the user:
// "Title (@username) — short description". The description is truncated to keep
// channel lists readable.
func formatChannel(c store.Channel) string {
	username := "@" + strings.TrimPrefix(strings.TrimSpace(c.Name), "@")
	title := strings.TrimSpace(c.Title)

	head := username
	if title != "" && !strings.EqualFold(title, username) {
		head = fmt.Sprintf("%s (%s)", title, username)
	}

	about := strings.TrimSpace(c.About)
	if about == "" {
		return head
	}
	return head + " — " + truncateText(about, 120)
}

func truncateText(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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

const helpText = `👋 Привет! Я ищу ответы по вашим Telegram-каналам.

Чтобы искать, мне нужен вход в ваш аккаунт: Telegram разрешает читать каналы только вам самим, поэтому я смотрю их от вашего имени. Нажмите «Войти» и отсканируйте QR-код.

🔒 Приватность: бот не сохраняет пароль и личные данные. Пароль двухфакторной аутентификации удаляется сразу после ввода. Хранится только список каналов — его можно удалить командой /delete_me.

Команды:
🔑 /qrlogin — войти через QR-код.
➕ /add_channels — добавить каналы.
➖ /remove_channels — убрать каналы.
📋 /list_channels — мои каналы.
🗑 /delete_me — удалить аккаунт и все данные.
❌ /cancel — отменить текущее действие.

После входа добавьте каналы и задайте вопрос текстом или голосом.`

// guestHelpText is shown before login: only the entry commands are relevant.
const guestHelpText = `👋 Привет! Я ищу ответы по вашим Telegram-каналам.

Чтобы искать, мне нужен вход в ваш аккаунт: Telegram разрешает читать каналы только вам самим, поэтому я смотрю их от вашего имени. Нажмите «Войти» и отсканируйте QR-код.

🔒 Приватность: бот не сохраняет пароль и личные данные. Пароль двухфакторной аутентификации удаляется сразу после ввода. Хранится только список каналов — его можно удалить командой /delete_me.

Команды:
🔑 /qrlogin — войти через QR-код.
❌ /cancel — отменить текущее действие.
ℹ️ /help — справка.`

var welcomeMarkup = &telego.InlineKeyboardMarkup{
	InlineKeyboard: [][]telego.InlineKeyboardButton{
		{
			{Text: "🔑 Войти", CallbackData: "qrlogin"},
			{Text: "ℹ️ Помощь", CallbackData: "help"},
		},
	},
}
