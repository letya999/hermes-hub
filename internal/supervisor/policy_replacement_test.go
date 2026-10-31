package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// A runtime spawned under policy A must not serve requests minted under
// policy B: the runtime's own envelope gate rejects them with 409 forever.
// Ensure must replace the generation instead of reusing it.
func TestEnsureReplacesRuntimeOnPolicyRotation(t *testing.T) {
	var mu sync.Mutex
	runs, removes := 0, 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		switch args[0] {
		case "inspect":
			return nil, os.ErrNotExist
		case "run":
			runs++
		case "rm":
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	old := binding(root)
	if _, err := m.Ensure(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if runs != 1 {
		t.Fatalf("initial runs=%d", runs)
	}
	mu.Unlock()
	updated := binding(root)
	updated.PolicyVersion = "policy-2"
	replaced, err := m.Ensure(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || removes == 0 {
		t.Fatalf("policy rotation reused stale runtime: runs=%d removes=%d", runs, removes)
	}
	if replaced.PolicyVersion != "policy-2" {
		t.Fatalf("replaced runtime recorded policy %q", replaced.PolicyVersion)
	}
	// A matching policy must keep reusing the warm generation.
	again, err := m.Ensure(context.Background(), updated)
	if err != nil {
		t.Fatal(err)
	}
	if again.Generation != replaced.Generation || runs != 2 {
		t.Fatalf("same-policy ensure spawned anew: %+v runs=%d", again, runs)
	}
}

// A pin written under an older policy must not respawn the runtime with the
// stale hash: ReconcilePins restamps the request to the space's current
// policy before acquiring.
func TestReconcilePinsRestampStalePinPolicy(t *testing.T) {
	var mu sync.Mutex
	var spawned []string
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) == 0 {
			return []byte("running"), nil
		}
		switch args[0] {
		case "inspect":
			return nil, os.ErrNotExist
		case "run":
			spawned = append(spawned, strings.Join(args, " "))
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	m.cfg.Environment = "prod"
	current := controlTestPolicy(t, root)
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-stale"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	if err := m.SetPin(request, true); err != nil {
		t.Fatal(err)
	}
	m.ReconcilePins(context.Background())
	key := pinKey(request)
	m.mu.Lock()
	pin, ok := m.pins[key]
	m.mu.Unlock()
	if !ok || pin.PolicyVersion != current {
		t.Fatalf("pin not restamped: %q ok=%v want %s", pin.PolicyVersion, ok, current)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(spawned) != 1 || !strings.Contains(spawned[0], "HUB_POLICY_VERSION="+current) {
		t.Fatalf("runtime spawned without current policy env: %v want %s", spawned, current)
	}
}

// A runtime admission conflict (policy envelope rejected by the runtime
// itself) is a terminal refusal, never an uncertain transport outcome:
// the job record must land in failed so callers stop retrying resume.
func TestRuntimeAdmissionConflictIsTerminal(t *testing.T) {
	runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "policy version mismatch"})
	}))
	defer runtimeAPI.Close()
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].Address = runtimeAPI.URL
	m.mu.Unlock()
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "job-409", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-409", Text: "hello"}
	body, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/execute", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"status":"failed"`) {
		t.Fatalf("conflict response=%d %s", rec.Code, rec.Body.String())
	}
	m.mu.Lock()
	record := m.jobs[request.JobID]
	m.mu.Unlock()
	if record.Status != "failed" || record.Response.LastEvent != "run.failed" {
		t.Fatalf("conflict recorded as %q/%q, want failed/run.failed", record.Status, record.Response.LastEvent)
	}
}
