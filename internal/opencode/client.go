package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client talks to a headless `opencode serve` instance over its HTTP API.
type Client struct {
	baseURL  string
	username string
	password string
	http     *http.Client
}

func New(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 10 * time.Minute},
	}
}

// Chat runs a single-turn agent interaction: it creates a session, sends the
// prompt, collects the assistant's text and cleans the session up.
func (c *Client) Chat(ctx context.Context, agent, model, prompt string) (string, error) {
	sessID, err := c.createSession(ctx)
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	defer c.deleteSession(context.Background(), sessID)

	return c.sendMessage(ctx, sessID, agent, model, prompt)
}

type sessionResp struct {
	ID string `json:"id"`
}

func (c *Client) createSession(ctx context.Context) (string, error) {
	var out sessionResp
	if err := c.do(ctx, http.MethodPost, "/session", nil, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("server returned empty session id")
	}
	return out.ID, nil
}

func (c *Client) deleteSession(ctx context.Context, id string) {
	_ = c.do(ctx, http.MethodDelete, "/session/"+id, nil, nil)
}

type part struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type messageResp struct {
	Parts []part `json:"parts"`
}

func (c *Client) sendMessage(ctx context.Context, sessID, agent, model, prompt string) (string, error) {
	body := map[string]any{
		"agent": agent,
		"parts": []map[string]any{
			{"type": "text", "text": prompt},
		},
	}
	if model != "" {
		body["model"] = model
	}

	var out messageResp
	if err := c.do(ctx, http.MethodPost, "/session/"+sessID+"/message", body, &out); err != nil {
		return "", err
	}

	var sb strings.Builder
	for _, p := range out.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var buf bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		buf = *bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.username != "" || c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("opencode returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
