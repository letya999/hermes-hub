package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestReadToolHubReconnectMarker(t *testing.T) {
	state := t.TempDir()
	if _, err := readToolHubReconnectMarker(state); err == nil {
		t.Fatal("missing marker accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	marker, err := readToolHubReconnectMarker(state)
	if err != nil || marker.Revision != 7 {
		t.Fatalf("marker=%+v err=%v", marker, err)
	}
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToolHubReconnectMarker(state); err == nil {
		t.Fatal("zero revision accepted")
	}
}

func TestRequestHermesMCPReloadAtIsIdempotentAndWaitsForCompletion(t *testing.T) {
	var polls atomic.Int32
	var input, idempotency, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			var body struct {
				Input string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			input = body.Input
			idempotency = r.Header.Get("Idempotency-Key")
			_, _ = w.Write([]byte(`{"run_id":"reload-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/reload-1":
			if polls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"status":"running"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"completed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	request := ExecuteRequest{
		Envelope: identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", ConversationID: "toolhub", DeliveryTargetID: "toolhub", PolicyVersion: "policy-1"},
		Text:     "/reload-mcp", IdempotencyKey: "toolhub-reconnect-7",
	}
	if err := requestHermesMCPReloadAt(context.Background(), server.URL, "secret", request); err != nil {
		t.Fatal(err)
	}
	if input != "/reload-mcp" || idempotency != request.IdempotencyKey || authorization != "Bearer secret" || polls.Load() < 2 {
		t.Fatalf("input=%q idempotency=%q auth=%q polls=%d", input, idempotency, authorization, polls.Load())
	}
	if strings.Contains(input+idempotency+authorization, "fixture-secret") {
		t.Fatal("reload request leaked a credential")
	}
}

func TestToolHubReconnectWatcherProcessesMarker(t *testing.T) {
	var runs atomic.Int32
	var idempotency atomic.Value
	idempotency.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/sessions":
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			runs.Add(1)
			idempotency.Store(r.Header.Get("Idempotency-Key"))
			_, _ = w.Write([]byte(`{"run_id":"watcher-run"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/watcher-run":
			_, _ = w.Write([]byte(`{"status":"completed"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_ENDPOINT", server.URL+"/mcp")
	t.Setenv("HUB_TOOLHUB_RECONNECT", "true")
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_HERMES_API_HOST", host)
	t.Setenv("HUB_HERMES_API_PORT", port)
	t.Setenv("API_SERVER_KEY", "secret")
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "toolhub-reconnect.request"), []byte(`{"revision":9}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startToolHubReconnectWatcher(ctx, state)
	deadline := time.Now().Add(3 * time.Second)
	for runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if runs.Load() != 1 {
		t.Fatalf("watcher runs=%d", runs.Load())
	}
	key := idempotency.Load().(string)
	if !strings.HasPrefix(key, "toolhub-reconnect-9-") {
		t.Fatalf("reconnect idempotency key is not session-bound: %q", key)
	}
}
