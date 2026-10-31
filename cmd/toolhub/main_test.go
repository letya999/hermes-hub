package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestRunRequiresStore(t *testing.T) {
	t.Setenv("HUB_TOOLHUB_STORE", "")
	if err := run(); err == nil {
		t.Fatal("missing ToolHub store accepted")
	}
}

func TestRunWithContextStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(path, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", path)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithContext(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("server exited before cancellation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server ignored cancellation")
	}
}

func TestRunRetriesConfirmedRemovalAfterBrokerRecovers(t *testing.T) {
	oldInterval := removalRetryInterval
	removalRetryInterval = 10 * time.Millisecond
	defer func() { removalRetryInterval = oldInterval }()
	path := filepath.Join(t.TempDir(), "store.json")
	store := toolhub.NewStore()
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-retry", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseCleanupIncomplete, Revision: 1, CreatedAt: time.Now(), BrokerCredentialID: "credential_1", Finalizers: []api.Finalizer{{Name: api.FinalizerBrokerCredential, State: api.FinalizerPending}}}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var allow atomic.Bool
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !allow.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer broker.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "broker.key")
	if err := os.WriteFile(keyPath, private, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", path)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	for _, prefix := range []string{"HUB_CREDENTIAL_BROKER_CONTROL_", "HUB_CREDENTIAL_BROKER_RUNTIME_"} {
		t.Setenv(prefix+"URL", broker.URL)
		t.Setenv(prefix+"KEY_FILE", keyPath)
		t.Setenv(prefix+"KEY_ID", "toolhub")
		t.Setenv(prefix+"ISSUER", "hermes-toolhub")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWithContext(ctx) }()
	// Generous deadline: startup under parallel -race load can take seconds
	// before the first removal attempt reaches the stub Broker.
	deadline := time.Now().Add(15 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls.Load() == 0 {
		t.Fatal("startup removal did not reach Broker")
	}
	allow.Store(true)
	for time.Now().Before(deadline) {
		reloaded, err := toolhub.Load(path)
		if err == nil {
			auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", ConversationID: "test", DeliveryTargetID: "test"}
			got, err := reloaded.OnboardingFor(auth, onboarding.OnboardingID)
			if err == nil && got.Phase == api.PhaseRemoved {
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Drain runWithContext before failing: a shutdown still writing the store
	// holds the .toolhub-* temp file open and trips TempDir cleanup on Windows.
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Fatal("removal did not retry after Broker recovery")
}

// #285: the single reconciler runs in production, not just tests — a
// connection whose credential ciphertext vanished from the backend must be
// demoted by the scheduled pass without any remove being in flight.
func TestRunSchedulesReconcilerPass(t *testing.T) {
	oldInterval := reconcileInterval
	reconcileInterval = 10 * time.Millisecond
	defer func() { reconcileInterval = oldInterval }()
	path := filepath.Join(t.TempDir(), "store.json")
	store := toolhub.NewStore()
	definition := api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://mcp.example.com/mcp", TLSMode: "required"},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "}},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "owner-scoped remote endpoint"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"mcp.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "loc-missing", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "cred.key")
	if err := os.WriteFile(keyFile, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(t.TempDir(), "secrets.json")
	t.Setenv("HUB_TOOLHUB_STORE", path)
	t.Setenv("HUB_CREDENTIAL_STORE", secretsPath)
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", keyFile)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runWithContext(ctx) }()
	// Generous deadline: startup under parallel -race load can take seconds
	// before the first reconciler pass writes the store.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		reloaded, err := toolhub.Load(path)
		if err == nil {
			connections := reloaded.Connections()
			if len(connections) == 1 && connections[0].Status == api.DegradedStatus {
				cancel()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Drain runWithContext before failing: a shutdown still writing the store
	// holds the .toolhub-* temp file open and trips TempDir cleanup on Windows.
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Fatal("scheduled reconciler did not degrade the dead-credential connection")
}

func TestRemovalRetryLoopStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		removalRetryLoop(ctx, 5*time.Millisecond, func(context.Context) error { calls.Add(1); return nil })
		close(done)
	}()
	deadline := time.After(time.Second)
	for calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("removal retry loop did not run")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("removal retry loop did not stop")
	}
}

func TestRemovalRetryLoopReportsFailure(t *testing.T) {
	var output bytes.Buffer
	log.SetOutput(&output)
	defer log.SetOutput(os.Stderr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	called := make(chan struct{}, 1)
	go func() {
		removalRetryLoop(ctx, 5*time.Millisecond, func(context.Context) error { called <- struct{}{}; return errors.New("retry failed") })
		close(done)
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("retry loop did not run")
	}
	cancel()
	<-done
	if !strings.Contains(output.String(), "retry failed") {
		t.Fatal("removal failure was swallowed")
	}
}

func TestRunResumesConfirmedRemovalBeforeListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := toolhub.NewStore()
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-retry", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseCleanupIncomplete, Revision: 1, CreatedAt: time.Now(), Finalizers: []api.Finalizer{{Name: api.FinalizerVerifyZero, State: api.FinalizerDone}}}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", path)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "invalid-listen-address")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	_ = run()
	reloaded, err := toolhub.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", ConversationID: "test", DeliveryTargetID: "test"}
	got, err := reloaded.OnboardingFor(auth, onboarding.OnboardingID)
	if err != nil || got.Phase != api.PhaseRemoved {
		t.Fatalf("removal did not resume at startup: %v %s", err, got.Phase)
	}
}

func TestRunRejectsInvalidListenAddressAfterLoadingStore(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "invalid-listen-address")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	if err := run(); err == nil {
		t.Fatal("invalid listen address accepted")
	}
}

// CHG-0129 S2.2: the public listener is opt-in — no HUB_PUBLIC_LISTEN means
// no second server; with it, the handler's public surface is served and
// control paths are absent.
func TestRunFailsOnUnusablePublicListen(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_PUBLIC_LISTEN", "no-such-host.invalid:80")
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("unusable public listen address accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() ignored an unusable public listen address")
	}
}

func TestRunServesPublicSurface(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(storePath, []byte(`{"schema":1,"definitions":[],"connections":[],"credential_references":[],"bindings":[],"workloads":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	publicAddr := free.Addr().String()
	free.Close()
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "runtime")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_TOOLHUB_LISTEN", "127.0.0.1:0")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_PUBLIC_LISTEN", publicAddr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runWithContext(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + publicAddr + "/confirm/x?t=t")
		if err == nil {
			resp.Body.Close()
			// Reaching the confirm handler with an unknown onboarding is a 401;
			// a refused connection means the public listener never started.
			if resp.StatusCode == http.StatusUnauthorized {
				return
			}
			t.Fatalf("public surface status=%d", resp.StatusCode)
		}
		if time.Now().After(deadline) {
			t.Fatalf("public listener never answered: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestPublicServerOptIn(t *testing.T) {
	t.Setenv("HUB_PUBLIC_LISTEN", "")
	if srv, ln, err := publicServerFor(handlerFunc{}); err != nil || srv != nil || ln != nil {
		t.Fatal("public listener should be absent without HUB_PUBLIC_LISTEN")
	}
	t.Setenv("HUB_PUBLIC_LISTEN", "127.0.0.1:0")
	if _, _, err := publicServerFor(handlerFunc{}); err == nil {
		t.Fatal("handler without a public surface accepted")
	}
	srv, ln, err := publicServerFor(withPublic{})
	if err != nil || srv == nil || ln == nil {
		t.Fatalf("public server=%v err=%v", srv, err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Shutdown(context.Background()) }()
	base := "http://" + ln.Addr().String()
	resp, err := http.Get(base + "/confirm/x?t=t")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("public surface status=%d", resp.StatusCode)
	}
	blocked, err := http.Get(base + "/v1/connectors")
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusNotFound {
		t.Fatalf("control path on public surface status=%d", blocked.StatusCode)
	}
}

type handlerFunc struct{}

func (handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {}

type withPublic struct{ handlerFunc }

func (withPublic) PublicHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/confirm/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	return mux
}
