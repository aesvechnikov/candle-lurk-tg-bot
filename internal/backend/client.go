package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/tg"
)

// Client is an HTTP client for the local backend, shared by the bot and the
// MCP server.
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(addr string) *Client {
	base := addr
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		http:    &http.Client{Timeout: 120 * time.Second},
	}
}

// LoginStart requests a login code. It returns "code" once the code has been
// requested from Telegram.
func (c *Client) LoginStart(ctx context.Context, userID int64, phone string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	err := c.post(ctx, "/login/start", map[string]any{"user_id": userID, "phone": phone}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Status, nil
}

// LoginCode submits the received code. It returns "password" if 2FA is
// required, or "done" on success.
func (c *Client) LoginCode(ctx context.Context, userID int64, code string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	err := c.post(ctx, "/login/code", map[string]any{"user_id": userID, "code": code}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Status, nil
}

// LoginPassword submits the 2FA password. It returns "done" on success.
func (c *Client) LoginPassword(ctx context.Context, userID int64, password string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	err := c.post(ctx, "/login/password", map[string]any{"user_id": userID, "password": password}, &resp)
	if err != nil {
		return "", err
	}
	return resp.Status, nil
}

// ImportSession stores a pre-generated session JSON for the user.
func (c *Client) ImportSession(ctx context.Context, userID int64, session string) error {
	return c.post(ctx, "/import", map[string]any{"user_id": userID, "session": session}, nil)
}

// QRLoginStart starts a QR login and returns the login URL to show the user.
func (c *Client) QRLoginStart(ctx context.Context, userID int64) (string, error) {
	var resp struct {
		URL string `json:"url"`
	}
	if err := c.post(ctx, "/qrlogin/start", map[string]any{"user_id": userID}, &resp); err != nil {
		return "", err
	}
	return resp.URL, nil
}

// QRLoginPoll waits for the QR login to progress. It returns the status
// ("done", "refresh" or "timeout") and, for "refresh", a new URL.
func (c *Client) QRLoginPoll(ctx context.Context, userID int64) (status, url string, err error) {
	var resp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
	}
	if err := c.post(ctx, "/qrlogin/poll", map[string]any{"user_id": userID}, &resp); err != nil {
		return "", "", err
	}
	return resp.Status, resp.URL, nil
}

// Search queries the backend for messages matching query in channels.
func (c *Client) Search(ctx context.Context, userID int64, channels []string, query string, limit int) ([]tg.Result, error) {
	var results []tg.Result
	err := c.post(ctx, "/search", map[string]any{
		"user_id":  userID,
		"channels": channels,
		"query":    query,
		"limit":    limit,
	}, &results)
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&e); err == nil && e.Error != "" {
			return fmt.Errorf("%s", e.Error)
		}
		return fmt.Errorf("backend returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode backend response: %w", err)
	}
	return nil
}
