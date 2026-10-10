package main

import (
	"log"
	"net/http"
	"time"

	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/backend"
	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/config"
	"github.com/aesvechnikov/candle-lurk-tg-bot/internal/tg"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	if cfg.APIID == 0 || cfg.APIHash == "" {
		log.Fatal("TG_API_ID and TG_API_HASH are required")
	}
	if cfg.SessionKey == nil {
		log.Fatal("SESSION_KEY is required (64 hex chars)")
	}

	mgr := tg.NewManager(cfg.APIID, cfg.APIHash, cfg.SessionKey, cfg.DataDir)
	srv := backend.NewServer(mgr)

	s := &http.Server{
		Addr:              cfg.BackendAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("backend listening on %s", cfg.BackendAddr)
	log.Fatal(s.ListenAndServe())
}
