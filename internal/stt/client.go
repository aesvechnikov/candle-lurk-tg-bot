package stt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the local Vosk STT service over HTTP.
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
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Transcribe sends raw 16 kHz mono s16le PCM and returns the recognized text.
func (c *Client) Transcribe(ctx context.Context, pcm []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transcribe", bytes.NewReader(pcm))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("stt status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Text, nil
}
