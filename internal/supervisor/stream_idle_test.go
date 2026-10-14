package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// A stream outliving the bounded client must still complete: the legacy
// one-shot timeout is gone from the streaming hop.
func TestRelayRunStreamOutlivesLegacyClientTimeout(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		enc := json.NewEncoder(w)
		emit := func(event hubruntime.ExecuteResponse) {
			event.JobID, event.RuntimeGeneration = "long-job", "g1"
			_ = enc.Encode(event)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
		emit(hubruntime.ExecuteResponse{SessionID: "s", RunID: "r", Status: "running", LastEvent: "run.admitted", EventID: "admitted"})
		time.Sleep(90 * time.Millisecond)
		emit(hubruntime.ExecuteResponse{SessionID: "s", RunID: "r", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "answer"})
	}))
	defer api.Close()
	m.cfg.HTTP = &http.Client{Timeout: 40 * time.Millisecond}
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "g1", Address: api.URL, State: Ready}, binding: normalized, auth: "secret", leases: map[string]Lease{}}
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", controlTestPolicy(t, root)), JobID: "long-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "long", Text: "private"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
	req.Header.Set("Accept", hubruntime.RunStreamContentType)
	rec := httptest.NewRecorder()
	m.execute(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "answer") {
		t.Fatalf("stream cut by the bounded client: %d %s", rec.Code, rec.Body.String())
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "completed" {
		t.Fatalf("record=%+v", record)
	}
}

// A silent runtime stream is abandoned at the idle deadline and the job lands
// as uncertain — never earlier, never hanging forever.
func TestRelayRunStreamIdleDeadlineCancels(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{JobID: "idle-job", IdempotencyKey: "idle"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	reader := hubruntime.IdleReader(&ctxReader{done: cancelled}, 30*time.Millisecond, func() { close(cancelled) })
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.relayRunStream(rec, request, runtime, reader)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("silent stream outlived the idle deadline")
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "uncertain" {
		t.Fatalf("idle stream ended as %q", record.Status)
	}
}

// Heartbeat keepalive frames are forwarded to the next hop but never touch
// the durable job record — a frame without the run identity must not trip the
// stream binding checks.
func TestHeartbeatNotPersistedOrDelivered(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	runtime, err := m.Ensure(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := hubruntime.ExecuteRequest{JobID: "hb-job", IdempotencyKey: "hb"}
	if _, _, err := m.beginJob(request); err != nil {
		t.Fatal(err)
	}
	if err := m.bindJobGeneration(request, runtime.Generation); err != nil {
		t.Fatal(err)
	}
	var stream strings.Builder
	enc := json.NewEncoder(&stream)
	for _, event := range []hubruntime.ExecuteResponse{
		{JobID: request.JobID, RuntimeGeneration: runtime.Generation, SessionID: "s", RunID: "r", Status: "running", LastEvent: "run.admitted", EventID: "admitted"},
		{JobID: request.JobID, RuntimeGeneration: runtime.Generation, Status: "running", LastEvent: "run.heartbeat"},
		{JobID: request.JobID, RuntimeGeneration: runtime.Generation, SessionID: "s", RunID: "r", Status: "completed", LastEvent: "run.completed", EventID: "terminal:completed", Text: "answer"},
	} {
		if err := enc.Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	m.relayRunStream(rec, request, runtime, strings.NewReader(stream.String()))
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "completed" {
		t.Fatalf("heartbeat broke the stream: %+v", record)
	}
	if !strings.Contains(rec.Body.String(), "run.heartbeat") {
		t.Fatal("keepalive was not forwarded to the next hop")
	}
}

type ctxReader struct {
	done chan struct{}
}

func (c *ctxReader) Read(p []byte) (int, error) {
	<-c.done
	return 0, errors.New("cancelled")
}
