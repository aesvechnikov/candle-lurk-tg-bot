package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/aesvechnikov/tg-lurk-ai-bot/internal/tg"
)

// Server exposes the MTProto backend over local HTTP. It is dialed by both
// the Telegram bot and the MCP server that OpenCode spawns.
type Server struct {
	mgr       *tg.Manager
	mu        sync.Mutex
	qrPending map[int64]*pendingQR
}

type qrData struct {
	url string
	png []byte
}

type pendingQR struct {
	ch      chan qrData
	passCh  chan string
	passReq chan struct{}
	done    chan error
	cancel  context.CancelFunc
}

func NewServer(mgr *tg.Manager) *Server {
	return &Server{
		mgr:       mgr,
		qrPending: map[int64]*pendingQR{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/qrlogin/start", s.handleQRStart)
	mux.HandleFunc("/qrlogin/poll", s.handleQRPoll)
	mux.HandleFunc("/qrlogin/password", s.handleQRPassword)
	mux.HandleFunc("/channel_info", s.handleChannelInfo)
	mux.HandleFunc("/search", s.handleSearch)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleQRStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if !decode(w, r, &req) {
		return
	}

	s.mu.Lock()
	if _, ok := s.qrPending[req.UserID]; ok {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, "qr login already in progress")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	pa := &pendingQR{
		ch:      make(chan qrData, 1),
		passCh:  make(chan string, 1),
		passReq: make(chan struct{}),
		done:    make(chan error, 1),
		cancel:  cancel,
	}
	s.qrPending[req.UserID] = pa
	s.mu.Unlock()

	go func() {
		pa.done <- s.mgr.QRLogin(ctx, req.UserID,
			func(url string, png []byte) {
				select {
				case pa.ch <- qrData{url: url, png: png}:
				default:
				}
			},
			func() { close(pa.passReq) },
			pa.passCh,
		)
	}()

	select {
	case d := <-pa.ch:
		writeQR(w, http.StatusOK, "qr", d)
	case err := <-pa.done:
		s.cleanupQR(req.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "done"})
		}
	case <-ctx.Done():
		s.cleanupQR(req.UserID)
		writeError(w, http.StatusRequestTimeout, "qr login timed out")
	}
}

func (s *Server) handleQRPoll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64 `json:"user_id"`
	}
	if !decode(w, r, &req) {
		return
	}

	pa := s.getQR(req.UserID)
	if pa == nil {
		writeError(w, http.StatusNotFound, "no qr login in progress")
		return
	}

	select {
	case err := <-pa.done:
		s.cleanupQR(req.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "done"})
		}
	case d := <-pa.ch:
		writeQR(w, http.StatusOK, "refresh", d)
	case <-pa.passReq:
		writeJSON(w, http.StatusOK, map[string]string{"status": "password"})
	case <-time.After(60 * time.Second):
		writeJSON(w, http.StatusOK, map[string]string{"status": "timeout"})
	}
}

// handleQRPassword submits the 2FA password for an in-progress QR login.
func (s *Server) handleQRPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID   int64  `json:"user_id"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}

	pa := s.getQR(req.UserID)
	if pa == nil {
		writeError(w, http.StatusNotFound, "no qr login in progress")
		return
	}

	select {
	case pa.passCh <- req.Password:
	default:
	}

	select {
	case err := <-pa.done:
		s.cleanupQR(req.UserID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
		} else {
			writeJSON(w, http.StatusOK, map[string]string{"status": "done"})
		}
	case <-time.After(90 * time.Second):
		writeError(w, http.StatusRequestTimeout, "login step timed out")
	}
}

// writeQR writes a QR login response with the token URL and a base64 PNG.
func writeQR(w http.ResponseWriter, code int, status string, d qrData) {
	png := ""
	if len(d.png) > 0 {
		png = base64.StdEncoding.EncodeToString(d.png)
	}
	writeJSON(w, code, map[string]string{"status": status, "url": d.url, "png": png})
}

func (s *Server) getQR(userID int64) *pendingQR {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.qrPending[userID]
}

func (s *Server) cleanupQR(userID int64) {
	s.mu.Lock()
	if pa, ok := s.qrPending[userID]; ok {
		pa.cancel()
		delete(s.qrPending, userID)
	}
	s.mu.Unlock()
}

func (s *Server) handleChannelInfo(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  int64  `json:"user_id"`
		Channel string `json:"channel"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Channel == "" {
		writeError(w, http.StatusBadRequest, "channel is required")
		return
	}
	title, about, err := s.mgr.ChannelInfo(r.Context(), req.UserID, req.Channel)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"title": title, "about": about})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID   int64    `json:"user_id"`
		Channels []string `json:"channels"`
		Query    string   `json:"query"`
		Limit    int      `json:"limit"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if len(req.Channels) == 0 {
		writeJSON(w, http.StatusOK, []tg.Result{})
		return
	}

	results, err := s.mgr.Search(r.Context(), req.UserID, req.Channels, req.Query, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
