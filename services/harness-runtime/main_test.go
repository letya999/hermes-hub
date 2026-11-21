package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The dev-surface message endpoint keeps its auth and validation contract:
// browser origins and missing/wrong tokens never reach the model path.
func TestRoutesGuardAndValidation(t *testing.T) {
	mux := routes("secret-token")

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz refused: %d", rec.Code)
	}

	post := func() *http.Request {
		return httptest.NewRequest(http.MethodPost, "/v1/messages",
			strings.NewReader(`{"space":"s","agent":"a","text":"hi"}`))
	}
	cases := []struct {
		name   string
		mutate func(*http.Request)
		want   int
	}{
		{"browser origin refused", func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
			r.Header.Set("Authorization", "Bearer secret-token")
			r.Header.Set("Content-Type", "application/json")
		}, http.StatusForbidden},
		{"missing token", func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
		}, http.StatusUnauthorized},
		{"wrong token", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer other")
			r.Header.Set("Content-Type", "application/json")
		}, http.StatusUnauthorized},
		{"wrong content type", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer secret-token")
			r.Header.Set("Content-Type", "text/plain")
		}, http.StatusUnsupportedMediaType},
		{"bad body", func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer secret-token")
			r.Header.Set("Content-Type", "application/json")
			r.Body = http.NoBody
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := post()
			tc.mutate(req)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	get.Header.Set("Authorization", "Bearer secret-token")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET on messages allowed: %d", getRec.Code)
	}
}

// answer joins streamed deltas; an unconfigured or failing endpoint returns a
// bounded error, never a panic.
func TestAnswerJoinsStreamedReply(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", model.URL)
	t.Setenv("HUB_MANAGED_MODEL_ID", "m")

	got, err := answer(context.Background(), "hi")
	if err != nil || got != "hello" {
		t.Fatalf("answer = %q, %v", got, err)
	}
}

func TestAnswerRejectsUnavailableEndpoint(t *testing.T) {
	t.Setenv("HUB_MANAGED_MODEL_URL", "")
	t.Setenv("HUB_MANAGED_MODEL_ID", "")
	if _, err := answer(context.Background(), "hi"); err == nil {
		t.Fatal("unconfigured endpoint answered")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	t.Setenv("HUB_MANAGED_MODEL_URL", bad.URL)
	t.Setenv("HUB_MANAGED_MODEL_ID", "m")
	if _, err := answer(context.Background(), "hi"); err == nil {
		t.Fatal("failing endpoint answered")
	}
}

// serve refuses to start without its trusted-mode env contract, and serves
// the guarded surface when it has it.
func TestServeRequiresTrustedEnv(t *testing.T) {
	t.Setenv("HUB_TRUSTED", "")
	if err := serve(context.Background()); err == nil {
		t.Fatal("serve ran untrusted")
	}
	t.Setenv("HUB_TRUSTED", "true")
	t.Setenv("HUB_LISTEN", "")
	if err := serve(context.Background()); err == nil {
		t.Fatal("serve ran without HUB_LISTEN")
	}
	t.Setenv("HUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_SERVICE_TOKEN", "")
	if err := serve(context.Background()); err == nil {
		t.Fatal("serve ran without HUB_SERVICE_TOKEN")
	}
}

func TestServeAnswersUntilCancelled(t *testing.T) {
	t.Setenv("HUB_TRUSTED", "true")
	t.Setenv("HUB_SERVICE_TOKEN", "tok")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	t.Setenv("HUB_LISTEN", addr)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	var rec *http.Response
	for time.Now().Before(deadline) {
		rec, err = http.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	_ = rec.Body.Close()
	if rec.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", rec.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("serve returned %v", err)
	}
}

// The tools stdio mode fails fast on bad flags and on an unopenable tools
// root rather than silently serving.
func TestRunToolsRejectsBadInput(t *testing.T) {
	if err := runTools(context.Background(), []string{"-nonsense"}); err == nil {
		t.Fatal("bad flag accepted")
	}
	if err := runTools(context.Background(), []string{"-workspace", filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing workspace accepted")
	}
}
