package supervisor

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOperatorPinSurvivesRestartWakesOnceAndPreventsIdleReap(t *testing.T) {
	var mu sync.Mutex
	starts, removes := 0, 0
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		switch args[0] {
		case "inspect":
			return nil, os.ErrNotExist
		case "run":
			starts++
		case "rm":
			removes++
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Text: "must not persist"}
	if err := m.SetPin(request, true); err != nil {
		t.Fatal(err)
	}
	if m.pins[pinKey(request)].Text != "" {
		t.Fatal("pin persisted prompt")
	}
	restarted, err := New(m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	m = restarted
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() { defer wg.Done(); m.ReconcilePins(context.Background()) }()
	}
	wg.Wait()
	runtime, ok, err := m.Status(binding(root))
	if err != nil || !ok || runtime.Leases != 0 || starts != 1 {
		t.Fatalf("pin wake: %+v starts=%d err=%v", runtime, starts, err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if removes != 0 {
		t.Fatal("pinned runtime reaped")
	}
	other := request
	other.ConversationID = "other"
	if err := m.SetPin(other, false); err == nil {
		t.Fatal("wrong conversation removed pin")
	}
	if err := m.SetPin(request, false); err != nil {
		t.Fatal(err)
	}
	if err := m.Reap(context.Background(), time.Unix(100, 0).Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if removes != 1 {
		t.Fatalf("unpinned idle runtime not reaped: %d", removes)
	}
	m.ReconcilePins(context.Background())
	if starts != 1 {
		t.Fatal("removed pin woke runtime")
	}
	if _, err := os.Stat(filepath.Join(root, "hermes")); err != nil {
		t.Fatal("pin cleanup removed home")
	}
}

func TestPinWriteFailureRollsBackDesiredState(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	original := m.statePath
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := m.SetPin(request, true); err == nil || len(m.pins) != 0 {
		t.Fatal("failed pin changed desired state")
	}
	m.statePath = original
	if err := m.SetPin(request, true); err != nil {
		t.Fatal(err)
	}
	m.statePath = filepath.Join(t.TempDir(), "missing", "state.json")
	if err := m.SetPin(request, false); err == nil || len(m.pins) != 1 {
		t.Fatal("failed unpin lost desired state")
	}
}

func TestPinHTTPRequiresPrivateAuthorizationAndExplicitEnabled(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	body, _ := json.Marshal(request)
	for _, tc := range []struct {
		method, auth, body string
		status             int
	}{
		{"POST", "", string(body), 401},
		{"GET", "secret", "", 405},
		{"POST", "secret", "{", 400},
		{"POST", "secret", string(body), 400},
		{"POST", "secret", strings.TrimSuffix(string(body), "}") + `,"enabled":true}`, 200},
	} {
		r := httptest.NewRequest(tc.method, "/v1/pins", strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		w := httptest.NewRecorder()
		m.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.method, w.Code, w.Body.String())
		}
	}
}
