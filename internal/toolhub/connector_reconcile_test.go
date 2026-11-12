package toolhub

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestBrokerEventsReconcileSubmissionAndResendExactLink(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "toolhub.private")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/events":
			_ = json.NewEncoder(w).Encode(brokerv1.Events{NextCursor: 1, Events: []brokerv1.Event{{Sequence: 1, Kind: "credential.ready", PrincipalID: "alice", ContextID: "alice", RequestID: "request_live"}}})
		case "/v1/requests/request_live":
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_live", Status: "ready", CredentialID: "credential_live", ContractID: "github-pat", ContractRevision: 1, AuthorizationURL: "https://broker.example/connect/request_live"})
		case "/v1/credentials/credential_live":
			_ = json.NewEncoder(w).Encode(brokerv1.Credential{Status: "active"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()
	var mu sync.Mutex
	var notices []map[string]any
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/prepare-delivery-status" && r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(map[string]string{"delivery_status": api.PrepareDeliveryQueued})
			return
		}
		var notice map[string]any
		if err := json.NewDecoder(r.Body).Decode(&notice); err != nil {
			t.Error(err)
		}
		mu.Lock()
		notices = append(notices, notice)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer channel.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", channel.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-control")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	definition := remoteDefinition()
	definition.DefinitionID = "github-mcp-server"
	definition.CredentialContractID = "github-pat"
	definition.CredentialContractRevision = 1
	definition.CredentialContractEnv = map[string]string{"GOOGLE_TOKEN": "GITHUB_PERSONAL_ACCESS_TOKEN"}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	onboarding := m13P3Onboarding(auth, "reconcile", "github-mcp-server", "https://github.com/github/github-mcp-server", api.PhaseAwaitingCreds)
	onboarding.BrokerRequestID = "request_live"
	onboarding.Initiator = &auth
	onboarding.BrokerContractID = "github-pat"
	onboarding.BrokerAuthorizationURL = "https://broker.example/connect/request_live"
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Broker: &credentialbroker.Config{URL: broker.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}, Now: time.Now}
	if err := c.ReconcileBrokerEvents(t.Context(), auth, false); err != nil {
		t.Fatal(err)
	}
	var polls sync.WaitGroup
	for i := 0; i < 2; i++ {
		polls.Add(1)
		go func() {
			defer polls.Done()
			result, err := c.InvokeConnector(context.Background(), auth, "connector_status", map[string]any{"name": onboarding.OnboardingID})
			if err != nil || result.State != api.OpStateAwaitingConfirm {
				t.Errorf("concurrent status: result=%+v err=%v", result, err)
			}
		}()
	}
	polls.Wait()
	mu.Lock()
	if len(notices) != 1 || notices[0]["phase"] != api.PhaseAwaitingConfirm {
		t.Fatalf("confirmation notice count=%d", len(notices))
	}
	mu.Unlock()
	for i := 0; i < 2; i++ {
		result, err := c.InvokeConnector(context.Background(), auth, "connector_edit", map[string]any{"name": onboarding.OnboardingID, "action": "resend"})
		if err != nil || !result.LinkSent || result.State != api.OpStateAwaitingConfirm {
			t.Fatalf("resend %d: result=%+v err=%v", i, result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notices) != 3 || notices[1]["event"] != "link-resent" || notices[1]["event_id"] == notices[2]["event_id"] {
		t.Fatalf("resend must queue two distinct events: count=%d", len(notices))
	}
}

func TestConnectorResendRequiresExactOwnerID(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	if _, err := c.InvokeConnector(t.Context(), auth, "connector_edit", map[string]any{"name": "github-mcp-server", "action": "resend"}); err == nil {
		t.Fatal("name selector unexpectedly accepted for resend")
	}
}

func TestConnectorStatusAmbiguousPendingNamesGiveExactPhases(t *testing.T) {
	store, auth, _, profile := managedStore(t)
	profile.Revision++
	profile.Connectors = api.ConnectorsPersonal
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"old", "new"} {
		onboarding := m13P3Onboarding(auth, suffix, "github-mcp-server", "https://github.com/github/github-mcp-server", api.PhaseAwaitingConfirm)
		if err := store.PutOnboarding(onboarding); err != nil {
			t.Fatal(err)
		}
	}
	result, err := (&ControlPlane{Store: store, Now: time.Now}).InvokeConnector(t.Context(), auth, "connector_status", map[string]any{"name": "github-mcp-server"})
	if err != nil || result.Code != api.OpCodeAmbiguousTarget || len(result.Candidates) != 2 {
		t.Fatalf("ambiguous pending name: result=%+v err=%v", result, err)
	}
	for _, candidate := range result.Candidates {
		if candidate.ID == "" || candidate.OnboardingPhase != api.PhaseAwaitingConfirm {
			t.Fatalf("candidate missing exact id/phase: %+v", candidate)
		}
	}
}

func TestConcurrentExpiredConfirmationIssuesOneProof(t *testing.T) {
	var mu sync.Mutex
	notices := 0
	channel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		notices++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer channel.Close()
	t.Setenv("HUB_COMMUNICATION_CONTROL_URL", channel.URL)
	t.Setenv("HUB_COMMUNICATION_AUTH", "test-control")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:9999")
	store := NewStore()
	auth := aliceAuth()
	onboarding := m13P3Onboarding(auth, "expired-proof", "test", "", api.PhaseAwaitingConfirm)
	onboarding.ConfirmationNonce = "expired"
	onboarding.ConfirmationExpires = time.Now().Add(-time.Minute)
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	c := &ControlPlane{Store: store, Now: time.Now}
	if err := c.ReconcileBrokerEvents(t.Context(), auth, false); err != nil {
		t.Fatal(err)
	}
	var polls sync.WaitGroup
	for i := 0; i < 2; i++ {
		polls.Add(1)
		go func() {
			defer polls.Done()
			copy := onboarding
			if err := c.refreshConfirmation(t.Context(), auth, &copy); err != nil {
				t.Errorf("refresh: %v", err)
			}
		}()
	}
	polls.Wait()
	mu.Lock()
	defer mu.Unlock()
	if notices != 1 {
		t.Fatalf("parallel proof notices = %d, want one", notices)
	}
}

func TestBrokerEventGapKeepsCursorUntilOwnedStateCommits(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "test-key")
	if err := os.WriteFile(key, private, 0600); err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/events":
			_ = json.NewEncoder(w).Encode(brokerv1.Events{Gap: true, EarliestAvailable: 50, NextCursor: 10})
		case "/v1/requests/request_gap":
			if !ready.Load() {
				w.WriteHeader(503)
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Request{ID: "request_gap", Status: "ready", CredentialID: "credential_gap"})
		default:
			t.Errorf("unexpected scope/path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s, auth := NewStore(), aliceAuth()
	o := m13P3Onboarding(auth, "gap", "github", "https://github.com/example/mcp", api.PhaseAwaitingCreds)
	o.BrokerRequestID = "request_gap"
	if err := s.PutOnboarding(o); err != nil {
		t.Fatal(err)
	}
	foreign := m13P3Onboarding(bobAuth(), "foreign-gap", "github", "https://github.com/example/mcp", api.PhaseAwaitingCreds)
	foreign.BrokerRequestID = "request_foreign"
	if err := s.PutOnboarding(foreign); err != nil {
		t.Fatal(err)
	}
	cursor := brokerCursorKey(auth, false)
	s.brokerCursors[cursor] = 10
	path := filepath.Join(t.TempDir(), "store.db")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	s.path = path
	t.Cleanup(func() { _ = s.CloseDB() })
	c := &ControlPlane{Store: s, Broker: &credentialbroker.Config{URL: server.URL, KeyFile: key, KeyID: "toolhub", Issuer: "hermes-toolhub"}}
	if err := c.ReconcileBrokerEvents(t.Context(), auth, false); err == nil {
		t.Fatal("503 hidden during gap repair")
	}
	if s.brokerCursors[cursor] != 10 {
		t.Fatal("cursor advanced past uncommitted owner state")
	}
	ready.Store(true)
	if err := c.ReconcileBrokerEvents(t.Context(), auth, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = loaded.CloseDB() })
	current, _ := loaded.OnboardingFor(auth, o.OnboardingID)
	if current.Phase != api.PhaseAwaitingConfirm || current.BrokerCredentialID != "credential_gap" || loaded.brokerCursors[cursor] != 49 {
		t.Fatal("gap repair/cursor did not survive reload")
	}
	other, _ := loaded.OnboardingFor(bobAuth(), foreign.OnboardingID)
	if other.Phase != api.PhaseAwaitingCreds {
		t.Fatal("gap repair changed another owner")
	}
}
