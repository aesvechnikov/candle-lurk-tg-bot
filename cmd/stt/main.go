//go:build vosk

package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	vosk "github.com/alphacep/vosk-api/go"
)

// stt is a small local speech-to-text HTTP service backed by Vosk. It is built
// separately with cgo (and the vosk build tag) and run as its own systemd unit.
func main() {
	modelPath := os.Getenv("STT_MODEL_DIR")
	if modelPath == "" {
		log.Fatal("STT_MODEL_DIR is required")
	}
	addr := os.Getenv("STT_ADDR")
	if addr == "" {
		addr = "127.0.0.1:2700"
	}

	vosk.SetLogLevel(-1)

	model, err := vosk.NewModel(modelPath)
	if err != nil {
		log.Fatalf("load vosk model: %v", err)
	}
	defer model.Free()

	log.Printf("stt listening on %s (model %s)", addr, modelPath)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/transcribe", func(w http.ResponseWriter, r *http.Request) {
		text, err := transcribe(model, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": text})
	})
	log.Fatal(http.ListenAndServe(addr, mux))
}

// transcribe recognizes the raw 16 kHz mono s16le PCM from the request body.
func transcribe(model *vosk.VoskModel, r *http.Request) (string, error) {
	pcm, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	if len(pcm) == 0 {
		return "", nil
	}

	rec, err := vosk.NewRecognizer(model, 16000)
	if err != nil {
		return "", err
	}
	defer rec.Free()

	rec.AcceptWaveform(pcm)

	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(rec.FinalResult()), &out); err != nil {
		return "", err
	}
	return out.Text, nil
}
