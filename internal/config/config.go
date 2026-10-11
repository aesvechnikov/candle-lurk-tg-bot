package config

import (
	"crypto/aes"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config aggregates environment-driven settings shared by the bot,
// the MTProto backend and the MCP server.
type Config struct {
	// Telegram bot API token (required for the bot binary).
	BotToken string

	// MTProto application credentials from https://my.telegram.org
	// (required for the backend binary).
	APIID   int
	APIHash string

	// SessionKey is the 32-byte AES-256 key used to encrypt user
	// MTProto sessions at rest (required for the backend binary).
	SessionKey []byte

	// OpenCode headless server connection settings (bot binary).
	OpenCodeURL      string
	OpenCodeUsername string
	OpenCodePassword string
	OpenCodeModel    string

	// DataDir holds the SQLite DB and encrypted session files.
	DataDir string

	// BackendAddr is host:port the backend listens on and the bot/MCP dial.
	BackendAddr string

	// AllowedUsers is the whitelist of Telegram usernames (without @) that may
	// use the bot. Empty means everyone is allowed.
	AllowedUsers []string

	// STTEnabled toggles voice-message transcription via the local Vosk STT
	// service. STTAddr is the address of that service.
	STTEnabled bool
	STTAddr    string
}

// Load reads configuration from environment variables. Missing optional
// values get zero values and are validated by the binaries that need them.
func Load() (Config, error) {
	cfg := Config{
		BotToken:         os.Getenv("BOT_TOKEN"),
		APIHash:          os.Getenv("TG_API_HASH"),
		OpenCodeURL:      getenv("OPENCODE_URL", "http://127.0.0.1:4096"),
		OpenCodeUsername: getenv("OPENCODE_USERNAME", "opencode"),
		// The bot authenticates to `opencode serve`; accept the shared
		// OPENCODE_SERVER_PASSWORD as a fallback so a single variable works
		// for both the bot and the server.
		OpenCodePassword: getenv("OPENCODE_PASSWORD", os.Getenv("OPENCODE_SERVER_PASSWORD")),
		OpenCodeModel:    getenv("OPENCODE_MODEL", "deepseek/deepseek-flash"),
		DataDir:          getenv("DATA_DIR", "./data"),
		BackendAddr:      getenv("BACKEND_ADDR", "127.0.0.1:9090"),
		AllowedUsers:     splitList(os.Getenv("ALLOWED_USERS")),
		STTEnabled:       os.Getenv("STT_ENABLED") == "true" || os.Getenv("STT_ENABLED") == "1",
		STTAddr:          getenv("STT_ADDR", "127.0.0.1:2700"),
	}

	if v := os.Getenv("TG_API_ID"); v != "" {
		id, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("TG_API_ID must be an integer: %w", err)
		}
		cfg.APIID = id
	}

	if v := os.Getenv("SESSION_KEY"); v != "" {
		key, err := parseSessionKey(v)
		if err != nil {
			return cfg, err
		}
		cfg.SessionKey = key
	}

	return cfg, nil
}

// parseSessionKey validates and decodes a hex-encoded 32-byte AES key.
func parseSessionKey(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("SESSION_KEY must be hex-encoded: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("SESSION_KEY must be 32 bytes (64 hex chars), got %d", len(b))
	}
	if _, err := aes.NewCipher(b); err != nil {
		return nil, fmt.Errorf("SESSION_KEY is not a valid AES key: %w", err)
	}
	return b, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// splitList splits a comma-separated list, trimming spaces and a leading "@".
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimPrefix(strings.TrimSpace(p), "@")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
