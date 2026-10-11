package tg

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
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

// DeleteSession removes the user's MTProto session file. A missing file is
// treated as already deleted (idempotent).
func (m *Manager) DeleteSession(userID int64) error {
	if err := os.Remove(m.sessionPath(userID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// QRLogin runs the Telegram QR-code login flow. onToken is called each time a
// login token is (re)generated, with the login URL and a PNG-encoded QR image.
// If the account has two-step verification, onPass is called once the QR is
// scanned and the 2FA password must be read from passCh. It blocks until the
// login completes or ctx is cancelled.
func (m *Manager) QRLogin(
	ctx context.Context,
	userID int64,
	onToken func(url string, png []byte),
	onPass func(),
	passCh <-chan string,
) error {
	_ = os.Remove(m.sessionPath(userID))

	st, err := NewEncryptedStorage(m.sessionPath(userID), m.sessKey)
	if err != nil {
		return err
	}

	dispatcher := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(dispatcher)

	client := telegram.NewClient(m.apiID, m.apiHash, telegram.Options{
		SessionStorage: st,
		UpdateHandler:  dispatcher,
		Logger:         gotdLogger{},
	})

	qrFlow := qrlogin.NewQR(client.API(), m.apiID, m.apiHash, qrlogin.Options{})

	log.Printf("qrlogin: start user=%d", userID)
	err = client.Run(ctx, func(ctx context.Context) error {
		_, err := qrFlow.Auth(ctx, loggedIn, func(ctx context.Context, token qrlogin.Token) error {
			onToken(token.URL(), renderQR(token))
			return nil
		})
		if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
			log.Printf("qrlogin: 2FA required user=%d", userID)
			if onPass != nil {
				onPass()
			}
			select {
			case pass := <-passCh:
				_, err = client.Auth().Password(ctx, pass)
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	})
	log.Printf("qrlogin: result user=%d err=%v", userID, err)
	return err
}

// renderQR renders a login token as a centered PNG QR image, or nil on
// failure. rsc.io/qr's Code.PNG() (unlike Code.Image()) centers the code with
// a proper quiet zone and scales each module, producing a scannable image.
func renderQR(token qrlogin.Token) []byte {
	code, err := qr.Encode(token.URL(), qr.M)
	if err != nil {
		return nil
	}
	return code.PNG()
}

func (m *Manager) newClient(userID int64) (*telegram.Client, error) {
	st, err := NewEncryptedStorage(m.sessionPath(userID), m.sessKey)
	if err != nil {
		return nil, err
	}
	return telegram.NewClient(m.apiID, m.apiHash, telegram.Options{
		SessionStorage: st,
		Logger:         gotdLogger{},
	}), nil
}

// Authorized reports whether the user has a valid MTProto session.
func (m *Manager) Authorized(ctx context.Context, userID int64) (bool, error) {
	if _, err := os.Stat(m.sessionPath(userID)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	client, err := m.newClient(userID)
	if err != nil {
		return false, err
	}
	authorized := false
	err = client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		authorized = status.Authorized
		return nil
	})
	return authorized, err
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
			return fmt.Errorf("user %d is not authorized, run /qrlogin first", userID)
		}

		api := client.API()
		minDate := searchMinDate()
		for _, raw := range channels {
			username := cleanUsername(raw)
			peer, linkUsername := resolveChannel(ctx, api, username)
			if peer == nil {
				continue
			}
			resp, err := api.MessagesSearch(ctx, &tg.MessagesSearchRequest{
				Peer:    peer,
				Q:       query,
				Filter:  &tg.InputMessagesFilterEmpty{},
				MinDate: minDate,
				Limit:   limit,
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

// searchMinDate returns the earliest message date channel search considers.
// It bounds how much history the model can pull, keeping results and token
// usage reasonable.
func searchMinDate() int {
	return int(time.Now().AddDate(0, -3, 0).Unix())
}

// ChannelInfo returns the title and description (about) of a channel, using
// the user's own session. The description may be empty.
func (m *Manager) ChannelInfo(ctx context.Context, userID int64, username string) (title, about string, err error) {
	client, err := m.newClient(userID)
	if err != nil {
		return "", "", err
	}
	err = client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("auth status: %w", err)
		}
		if !status.Authorized {
			return fmt.Errorf("user %d is not authorized, run /qrlogin first", userID)
		}
		title, about, err = channelInfo(ctx, client.API(), cleanUsername(username))
		return err
	})
	return title, about, err
}

// channelInfo resolves a username and fetches its title and description.
func channelInfo(ctx context.Context, api *tg.Client, username string) (title, about string, err error) {
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return "", "", err
	}
	for _, chat := range resolved.Chats {
		ch, ok := chat.(*tg.Channel)
		if !ok {
			continue
		}
		title = ch.Title
		full, err := api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash})
		if err != nil {
			return title, "", nil
		}
		if cf, ok := full.FullChat.(*tg.ChannelFull); ok {
			about = cf.About
		}
		return title, about, nil
	}
	return "", "", fmt.Errorf("channel %q not found", username)
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
