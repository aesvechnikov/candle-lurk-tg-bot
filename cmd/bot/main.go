package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/backend"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/bot"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/config"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/opencode"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/store"
	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/stt"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.BotToken == "" {
		log.Fatal("BOT_TOKEN is required")
	}
	if cfg.OpenCodePassword == "" {
		log.Fatal("OPENCODE_PASSWORD is required")
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatal(err)
	}

	st, err := store.Open(filepath.Join(cfg.DataDir, "bot.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	backendClient := backend.NewClient(cfg.BackendAddr)
	oc := opencode.New(cfg.OpenCodeURL, cfg.OpenCodeUsername, cfg.OpenCodePassword)

	var sttClient *stt.Client
	if cfg.STTEnabled {
		sttClient = stt.NewClient(cfg.STTAddr)
	}

	b, err := bot.New(cfg.BotToken, st, backendClient, oc, cfg.OpenCodeModel, cfg.AllowedUsers, cfg.AdminUsers, sttClient, cfg.STTEnabled)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Println("bot starting")
	if err := b.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
