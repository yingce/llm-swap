package testutil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
)

type FakeLlamaSwap struct {
	server *httptest.Server
}

func NewFakeLlamaSwap() *FakeLlamaSwap {
	fake := &FakeLlamaSwap{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /running", fake.handleRunning)
	mux.HandleFunc("POST /v1/chat/completions", fake.handleChatCompletions)
	mux.HandleFunc("POST /api/models/unload/qwen", fake.handleUnloadQwen)
	fake.server = httptest.NewServer(mux)
	return fake
}

func (f *FakeLlamaSwap) URL() string {
	return f.server.URL
}

func (f *FakeLlamaSwap) Close() {
	f.server.Close()
}

func (f *FakeLlamaSwap) handleRunning(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"running": []map[string]string{
			{"model": "qwen", "state": "ready"},
		},
	})
}

func (f *FakeLlamaSwap) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1,
		"model":   "qwen",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "ok",
				},
				"finish_reason": "stop",
			},
		},
	})
}

func (f *FakeLlamaSwap) handleUnloadQwen(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}
