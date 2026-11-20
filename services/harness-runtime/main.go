// harness-runtime is the v1 space workload: it answers messages for its
// (space, agent) through the managed model endpoint. Spawned and supervised
// by spawn-controller; refuses untrusted installations.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var modelClient = &http.Client{Timeout: 60 * time.Second}

// answer sends the message to the managed model endpoint and joins the
// streamed deltas into the reply text.
func answer(ctx context.Context, text string) (string, error) {
	base := strings.TrimRight(os.Getenv("HUB_MANAGED_MODEL_URL"), "/")
	model := os.Getenv("HUB_MANAGED_MODEL_ID")
	if base == "" || model == "" {
		return "", errors.New("model endpoint is not configured")
	}
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": text}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := modelClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("model: %s", http.StatusText(resp.StatusCode))
	}
	var reply strings.Builder
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
	for scanner.Scan() {
		line, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		if line == "[DONE]" {
			return reply.String(), nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			reply.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return reply.String(), nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// guard applies the shared service-surface checks: a browser Origin is
// refused outright, then the shared service token must match in constant
// time. /healthz stays open for the launcher probe.
func guard(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "browser origin refused"})
			return
		}
		got, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !bearer || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func routes(token string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/v1/messages", guard(token, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
			return
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			writeJSON(w, http.StatusUnsupportedMediaType, map[string]any{"error": "application/json required"})
			return
		}
		var input struct {
			Space string `json:"space"`
			Agent string `json:"agent"`
			Text  string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil || input.Space == "" || input.Agent == "" || input.Text == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid message"})
			return
		}
		reply, err := answer(r.Context(), input.Text)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": "model unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reply": reply})
	}))
	return mux
}

func serve(ctx context.Context) error {
	if os.Getenv("HUB_TRUSTED") != "true" {
		return errors.New("untrusted installation")
	}
	listen := os.Getenv("HUB_LISTEN")
	if listen == "" {
		return errors.New("HUB_LISTEN is required")
	}
	token := os.Getenv("HUB_SERVICE_TOKEN")
	if token == "" {
		return errors.New("HUB_SERVICE_TOKEN is required")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: routes(token), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "harness-runtime:", err)
		os.Exit(1)
	}
}
