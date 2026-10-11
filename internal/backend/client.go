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

// QRLoginStart starts a QR login and returns the login URL and a base64 PNG
// of the QR code to show the user.
func (c *Client) QRLoginStart(ctx context.Context, userID int64) (url, pngBase64 string, err error) {
	var resp struct {
		URL string `json:"url"`
		PNG string `json:"png"`
	}
	if err := c.post(ctx, "/qrlogin/start", map[string]any{"user_id": userID}, &resp); err != nil {
		return "", "", err
	}
	return resp.URL, resp.PNG, nil
}

// QRLoginPoll waits for the QR login to progress. It returns the status
// ("done", "refresh" or "timeout") and, for "refresh", a new URL and PNG.
func (c *Client) QRLoginPoll(ctx context.Context, userID int64) (status, url, pngBase64 string, err error) {
	var resp struct {
		Status string `json:"status"`
		URL    string `json:"url"`
		PNG    string `json:"png"`
	}
	if err := c.post(ctx, "/qrlogin/poll", map[string]any{"user_id": userID}, &resp); err != nil {
		return "", "", "", err
	}
	return resp.Status, resp.URL, resp.PNG, nil
}

// QRLoginPassword submits the 2FA password for an in-progress QR login.
func (c *Client) QRLoginPassword(ctx context.Context, userID int64, password string) (string, error) {
	var resp struct {
		Status string `json:"status"`
	}
	if err := c.post(ctx, "/qrlogin/password", map[string]any{"user_id": userID, "password": password}, &resp); err != nil {
		return "", err
	}
	return resp.Status, nil
}

// ChannelInfo returns the title and description of a channel.
func (c *Client) ChannelInfo(ctx context.Context, userID int64, channel string) (title, about string, err error) {
	var resp struct {
		Title string `json:"title"`
		About string `json:"about"`
	}
	if err := c.post(ctx, "/channel_info", map[string]any{"user_id": userID, "channel": channel}, &resp); err != nil {
		return "", "", err
	}
	return resp.Title, resp.About, nil
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
