package testkit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ModelServer returns an OpenAI chat/completions SSE fixture. Every response
// streams the configured chunks and a terminal [DONE], without external calls.
func ModelServer(t *testing.T, chunks ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model    string            `json:"model"`
			Stream   bool              `json:"stream"`
			Messages []json.RawMessage `json:"messages"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || !request.Stream || request.Model == "" || len(request.Messages) == 0 {
			http.Error(w, "invalid model fixture request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flush := w.(http.Flusher)
		for _, chunk := range chunks {
			body, _ := json.Marshal(map[string]any{"id": "fixture-completion", "object": "chat.completion.chunk", "model": request.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": chunk}, "finish_reason": nil}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", body)
			flush.Flush()
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"fixture-completion\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		flush.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}
