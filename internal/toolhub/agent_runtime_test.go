package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/audit"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passportview"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestAgentRuntimePreflightReleasesFenceAndReauthorizes(t *testing.T) {
	for _, mode := range []string{"success", "revoke", "unavailable", "executor-failure"} {
		t.Run(mode, func(t *testing.T) {
			store := NewStore()
			definition := agentDefinition()
			if err := store.RegisterDefinition(definition); err != nil {
				t.Fatal(err)
			}
			auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
			binding, err := store.Enable(auth, definition.DefinitionID, definition.Version)
			if err != nil {
				t.Fatal(err)
			}
			acquired, released, executed := false, false, false
			gateway := &Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil }, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
				if !acquired || released {
					t.Fatal("business dispatch has no active runtime lease")
				}
				executed = true
				if mode == "executor-failure" {
					return api.BackendResult{}, errors.New("channel lost")
				}
				return api.BackendResult{Structured: map[string]any{"ok": true}}, nil
			})}
			gateway.acquireRuntime = func(context.Context, api.EffectiveBinding) (func(), error) {
				if !store.mu.TryLock() {
					t.Fatal("cold startup still holds the admission registry lock")
				}
				store.mu.Unlock()
				if mode == "unavailable" {
					return nil, api.ErrIsolation
				}
				acquired = true
				if mode == "revoke" {
					if err := store.Disable(auth, definition.DefinitionID, definition.Version); err != nil {
						t.Fatal(err)
					}
				}
				return func() { released = true }, nil
			}
			out, err := gateway.CallAuthorized(t.Context(), auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, "file_read"), map[string]any{"path": "probe.txt"})
			switch mode {
			case "success":
				if err != nil || out == nil || !executed || !released {
					t.Fatalf("cold dispatch failed: %v", err)
				}
			case "revoke":
				if err == nil || out != nil || executed || !released || errors.Is(err, audit.ErrUncertain) {
					t.Fatal("revocation during cold startup reached the business executor")
				}
			case "unavailable":
				if !errors.Is(err, api.ErrBeforeEffect) || errors.Is(err, audit.ErrUncertain) || executed || released {
					t.Fatal("startup failure was misreported as an uncertain execution")
				}
			case "executor-failure":
				if !errors.Is(err, audit.ErrUncertain) || !executed || !released {
					t.Fatal("post-dispatch uncertainty or cleanup was lost")
				}
			}
			if binding.ToolBindingID == "" {
				t.Fatal("missing binding")
			}
		})
	}
}

func TestAgentRuntimeDispatchDoesNotPinRegistry(t *testing.T) {
	store := NewStore()
	definition := agentDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if _, err := store.Enable(auth, definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	gateway := &Gateway{Store: store, AuditWrite: func(string, map[string]string) error { return nil }, Backend: backendFunc(func(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
		close(entered)
		<-release
		return api.BackendResult{Text: "ok"}, nil
	})}
	gateway.acquireRuntime = func(context.Context, api.EffectiveBinding) (func(), error) { return func() {}, nil }
	go func() {
		_, err := gateway.CallAuthorized(t.Context(), auth, api.ProjectedToolName(definition.DefinitionID, definition.Version, "file_read"), map[string]any{"path": "probe.txt"})
		finished <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("tool backend did not start")
	}
	if !store.mu.TryLock() {
		close(release)
		t.Fatal("long agent tool call pinned the registry")
	}
	store.mu.Unlock()
	decided := make(chan struct{})
	go func() {
		_, _ = store.Decide(auth, "models", "use", "", time.Now())
		close(decided)
	}()
	select {
	case <-decided:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("rights decision blocked behind agent tool")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestAgentRuntimeLeaseUsesEnrolledPublishedIdentity(t *testing.T) {
	for _, scopeKind := range []string{"user", "group"} {
		t.Run(scopeKind, func(t *testing.T) {
			auth := identity.TelegramEnvelope("alice", 7, "alice", "policy-1")
			auth.Environment, auth.CapabilityProfile, auth.Generation = "prod", "alice-default", 1
			root := t.TempDir()
			revision := filepath.Join(root, "alice", "passports", "r-test")
			if err := os.MkdirAll(revision, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range passportview.Names {
				body := fmt.Sprintf("apiVersion: six-passport/v2\nkind: %s\nmetadata: {space: alice, environment: prod, revision: r-test}\n", name)
				if name == "stack" {
					body += "identity: {user: alice, kind: " + scopeKind + "}\n"
				}
				if err := os.WriteFile(filepath.Join(revision, name+".yaml"), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "alice", "passports", "current"), []byte("r-test\n"), 0600); err != nil {
				t.Fatal(err)
			}
			effective := api.EffectiveBinding{Binding: api.ToolBinding{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", PolicyVersion: "policy-1"}}
			container, err := api.ManagedAgentContainer(effective)
			if err != nil {
				t.Fatal(err)
			}
			var created, released, redirected atomic.Int32
			var badReceipt, redirect atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(500) }))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer service" {
					t.Error("missing service authority")
					w.WriteHeader(401)
					return
				}
				if r.Method == http.MethodDelete && r.URL.Path == "/v1/leases/lease-1" {
					released.Add(1)
					w.WriteHeader(200)
					return
				}
				if r.Method != http.MethodPost || r.URL.Path != "/v1/leases" {
					t.Error("unexpected lifecycle request")
					w.WriteHeader(404)
					return
				}
				if redirect.Load() {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				var body struct {
					identity.Envelope
					UserID  string `json:"user_id"`
					ActorID string `json:"actor_id"`
					Scope   string `json:"scope_id"`
					Kind    string `json:"kind"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || !body.Envelope.Equal(auth) || body.UserID != "alice" || body.ActorID != "alice" || body.Scope != scopeKind+":alice" || body.Kind != "lifecycle" {
					t.Error("caller or scope was not bound to enrollment")
				}
				created.Add(1)
				generation := "gen-1"
				if badReceipt.Load() {
					generation = "foreign-generation"
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"lease": map[string]any{"id": "lease-1", "kind": "lifecycle", "principal_id": "alice", "context_id": "alice", "runtime_mode": "gateway", "generation": "gen-1"}, "runtime": map[string]any{"principal_id": "alice", "context_id": "alice", "runtime_id": "alice", "generation": generation, "container": container}})
			}))
			defer server.Close()
			t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", server.URL)
			gateway := &Gateway{Tokens: map[string]identity.Envelope{"runtime-token": auth}, PassportRoot: root, ControlToken: "service"}
			ctx, cancel := context.WithCancel(t.Context())
			release, err := gateway.acquireAgentRuntime(ctx, effective)
			if err != nil || release == nil {
				t.Fatalf("acquire: %v", err)
			}
			cancel()
			release()
			if created.Load() != 1 || released.Load() != 1 {
				t.Fatal("cancelled business context leaked its runtime lease")
			}
			foreign := effective
			foreign.Binding.ContextID = "bob"
			if _, err := gateway.acquireAgentRuntime(t.Context(), foreign); !errors.Is(err, api.ErrUnauthorized) || created.Load() != 1 {
				t.Fatal("foreign binding reached the supervisor")
			}
			badReceipt.Store(true)
			if _, err := gateway.acquireAgentRuntime(t.Context(), effective); err == nil || released.Load() != 1 {
				t.Fatal("foreign generation receipt was accepted or released")
			}
			redirect.Store(true)
			if _, err := gateway.acquireAgentRuntime(t.Context(), effective); err == nil || redirected.Load() != 0 {
				t.Fatal("service bearer followed a lifecycle redirect")
			}
		})
	}
}
