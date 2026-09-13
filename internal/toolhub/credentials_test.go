package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stopRecorder struct{ ids []string }

func (s *stopRecorder) Stop(id string) error {
	s.ids = append(s.ids, id)
	return nil
}

func runningWorkload(t *testing.T, binding ToolBinding) WorkloadInstance {
	t.Helper()
	owner := &OwnerRef{Type: ContextOwner, ID: "alice"}
	workload, err := NewWorkloadInstance(binding, owner, "", 1, time.Now().UTC(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	workload.Status = RunningStatus
	if err := workload.Validate(); err != nil {
		t.Fatal(err)
	}
	return workload
}

func TestRotateDisableRemoveCutOpenSessionBeforeBackend(t *testing.T) {
	store, auth, binding := seededStore(t)
	if err := store.PutWorkloadInstance(runningWorkload(t, binding)); err != nil {
		t.Fatal(err)
	}
	stops := &stopRecorder{}
	store.Stopper = stops
	var calls atomic.Int32
	handler, err := (&Gateway{
		Store:  store,
		Tokens: map[string]identity.Envelope{"01234567890123456789012345678901": auth},
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			calls.Add(1)
			return BackendResult{Text: "provider"}, nil
		}),
	}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "open-session", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: "01234567890123456789012345678901"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("open session list=%+v err=%v", tools, err)
	}
	name := ProjectedToolName("google-work", "1.0.0", "search")
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "ok"}}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/rotated", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "after-rotate"}}); err == nil {
		t.Fatal("rotated credential was executed on open session")
	}
	if calls.Load() != 1 {
		t.Fatalf("backend ran after rotate: %d", calls.Load())
	}
	if len(stops.ids) == 0 {
		t.Fatal("affected workload was not stopped")
	}
}

func TestDisableRemoveAndDegradedFailClosed(t *testing.T) {
	projected := ProjectedToolName("google-work", "1.0.0", "search")
	store, auth, _ := seededStore(t)
	if err := store.SetConnectionStatus("google-work", DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.AuthorizeProjected(auth, projected, func(ProjectedTool, EffectiveBinding) error {
		t.Fatal("disabled connection admitted")
		return nil
	}); err == nil {
		t.Fatal("disabled connection admitted")
	}
	store2, auth2, _ := seededStore(t)
	if err := store2.SetConnectionStatus("google-work", RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store2.AuthorizeProjected(auth2, projected, func(ProjectedTool, EffectiveBinding) error {
		t.Fatal("revoked connection admitted")
		return nil
	}); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked connection: %v", err)
	}
	store3, auth3, _ := seededStore(t)
	if err := store3.MarkDegraded("google-work"); err != nil {
		t.Fatal(err)
	}
	if err := store3.AuthorizeProjected(auth3, projected, func(ProjectedTool, EffectiveBinding) error {
		t.Fatal("degraded connection admitted")
		return nil
	}); !errors.Is(err, ErrDegraded) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("degraded: %v", err)
	}
	catalog, err := store3.Catalog(auth3)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(catalog)
	if strings.Contains(strings.ToLower(string(body)), "super-secret") {
		t.Fatal(string(body))
	}
}

func TestTerminalExposureFlagIsReversible(t *testing.T) {
	store, _, _ := seededStore(t)
	if err := store.SetTerminalExposure("google-work", true); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	enabled := store.connections["google-work"].TerminalExposure
	store.mu.RUnlock()
	if !enabled {
		t.Fatal("terminal exposure not enabled")
	}
	if err := store.SetTerminalExposure("google-work", false); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	enabled = store.connections["google-work"].TerminalExposure
	store.mu.RUnlock()
	if enabled {
		t.Fatal("terminal exposure not reversed")
	}
}

func TestAuditWriteFailureDeniesCall(t *testing.T) {
	store, auth, _ := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	var calls atomic.Int32
	gateway := Gateway{
		Store: store,
		Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			calls.Add(1)
			return BackendResult{Text: "nope"}, nil
		}),
		AuditWrite: func(string, map[string]string) error { return errors.New("ledger down") },
	}
	if _, err := gateway.call(context.Background(), auth, name, nil); err == nil {
		t.Fatal("call succeeded without audit")
	}
	if calls.Load() != 0 {
		t.Fatal("backend ran after audit failure")
	}
}

func TestAuditFieldsIncludeRevision(t *testing.T) {
	store, auth, _ := seededStore(t)
	effective, err := store.Resolve(auth, DeterministicBindingID(auth.PrincipalID, auth.ContextID, auth.RuntimeID, "google-work", "1.0.0", "google-work", CredentialReferenceID("google-work", 1)))
	if err != nil {
		t.Fatal(err)
	}
	fields := auditFields(auth, ProjectedTool{BindingID: effective.Binding.ToolBindingID}, effective, "allow")
	if fields["credential_revision"] != "1" || fields["connection_revision"] != "1" || fields["policy_version"] != auth.PolicyVersion {
		t.Fatalf("audit fields=%v", fields)
	}
}
