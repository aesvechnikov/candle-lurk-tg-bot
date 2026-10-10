package tg

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

// Result is a single search hit returned to the caller.
type Result struct {
	Channel   string `json:"channel"`
	Username  string `json:"username,omitempty"`
	MessageID int    `json:"message_id"`
	Date      int    `json:"date"`
	Text      string `json:"text"`
	Link      string `json:"link,omitempty"`
}

// Manager wraps gotd clients: one encrypted session per Telegram user id.
type Manager struct {
	apiID   int
	apiHash string
	sessKey []byte
	dataDir string
}

func NewManager(apiID int, apiHash string, sessKey []byte, dataDir string) *Manager {
	return &Manager{apiID: apiID, apiHash: apiHash, sessKey: sessKey, dataDir: dataDir}
}

func (m *Manager) sessionPath(userID int64) string {
	return filepath.Join(m.dataDir, "sessions", fmt.Sprintf("%d.session", userID))
}

func (m *Manager) newClient(userID int64) (*telegram.Client, error) {
	st, err := NewEncryptedStorage(m.sessionPath(userID), m.sessKey)
	if err != nil {
		return nil, err
	}
	return telegram.NewClient(m.apiID, m.apiHash, telegram.Options{
		SessionStorage: st,
	}), nil
}

// interactiveUserAuth implements auth.UserAuthenticator, feeding the phone,
// code and 2FA password from channels controlled by the HTTP login flow.
type interactiveUserAuth struct {
	phone  string
	codeCh <-chan string
	passCh <-chan string
	onCode func()
	onPass func()
}

func (a *interactiveUserAuth) Phone(_ context.Context) (string, error) { return a.phone, nil }

func (a *interactiveUserAuth) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	if a.onCode != nil {
		a.onCode()
	}
	log.Printf("login: code requested, sent_code_type=%s phone_code_hash=%s", sentCode.TypeName(), sentCode.PhoneCodeHash)
	select {
	case code := <-a.codeCh:
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *interactiveUserAuth) Password(ctx context.Context) (string, error) {
	if a.onPass != nil {
		a.onPass()
	}
	log.Printf("login: 2FA password requested")
	select {
	case p := <-a.passCh:
		return p, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *interactiveUserAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return fmt.Errorf("telegram terms-of-service confirmation is not supported by the bot")
}

func (a *interactiveUserAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("telegram sign-up is not supported by the bot")
}

// Login authenticates the user's MTProto session. It blocks until the code
// (and, if needed, the 2FA password) is fed through the provided channels.
func (m *Manager) Login(
	ctx context.Context,
	userID int64,
	phone string,
	codeCh, passCh <-chan string,
	onCode, onPass func(),
) error {
	client, err := m.newClient(userID)
	if err != nil {
		return err
	}
	log.Printf("login: start user=%d phone=%q", userID, phone)
	ua := &interactiveUserAuth{phone: phone, codeCh: codeCh, passCh: passCh, onCode: onCode, onPass: onPass}
	flow := auth.NewFlow(ua, auth.SendCodeOptions{})
	err = client.Run(ctx, func(ctx context.Context) error {
		return client.Auth().IfNecessary(ctx, flow)
	})
	log.Printf("login: result user=%d err=%v", userID, err)
	return err
}

// Search looks for the given query in the listed channels using the user's
// own MTProto session and returns up to limit hits per channel.
func (m *Manager) Search(
	ctx context.Context,
	userID int64,
	channels []string,
	query string,
	limit int,
) ([]Result, error) {
	if limit <= 0 {
		limit = 20
	}
	client, err := m.newClient(userID)
	if err != nil {
		return nil, err
	}

	var results []Result
	err = client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("user %d is not authorized, run /login first", userID)
		}

		api := client.API()
		for _, raw := range channels {
			username := cleanUsername(raw)
			peer, linkUsername := resolveChannel(ctx, api, username)
			if peer == nil {
				continue
			}
			resp, err := api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
				Peer:   peer,
				Q:      query,
				Filter: &tg.InputMessagesFilterEmpty{},
				Limit:  limit,
			})
			if err != nil {
				continue
			}
			for _, msg := range extractMessages(resp) {
				if strings.TrimSpace(msg.Message) == "" {
					continue
				}
				results = append(results, Result{
					Channel:   raw,
					Username:  linkUsername,
					MessageID: msg.ID,
					Date:      msg.Date,
					Text:      msg.Message,
					Link:      messageLink(linkUsername, msg.ID),
				})
			}
		}
		return nil
	})
	return results, err
}

func cleanUsername(raw string) string {
	return strings.TrimPrefix(strings.TrimSpace(raw), "@")
}

// resolveChannel resolves a username to an InputPeerChannel and returns the
// channel's public username (for building t.me links).
func resolveChannel(ctx context.Context, api *tg.Client, username string) (tg.InputPeerClass, string) {
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return nil, ""
	}
	for _, chat := range resolved.Chats {
		ch, ok := chat.(*tg.Channel)
		if !ok {
			continue
		}
		return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}, ch.Username
	}
	return nil, ""
}

func extractMessages(c tg.MessagesMessagesClass) []*tg.Message {
	modified, ok := c.AsModified()
	if !ok {
		return nil
	}
	var out []*tg.Message
	for _, m := range modified.GetMessages() {
		if msg, ok := m.(*tg.Message); ok {
			out = append(out, msg)
		}
	}
	return out
}

func messageLink(username string, msgID int) string {
	if username == "" {
		return ""
	}
	return fmt.Sprintf("https://t.me/%s/%d", username, msgID)
}
