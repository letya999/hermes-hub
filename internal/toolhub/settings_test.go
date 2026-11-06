package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestSettingsBackendUsesPublishedPassportAndAuthenticatedPublisher(t *testing.T) {
	root := t.TempDir()
	writeMemoryPassport(t, root, "alice", false)
	revision := "r-0123456789abcdef"
	config := filepath.Join(root, "alice", "passports", revision, "config.yaml")
	if err := os.WriteFile(config, []byte("apiVersion: six-passport/v2\nkind: config\nmetadata: {space: alice, environment: dev, revision: "+revision+"}\nmemory: {enabled: false}\nruntime:\n  compression: {enabled: true}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var serverCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls.Add(1)
		if r.URL.Path != "/v1/settings" || r.Header.Get("Authorization") != "Bearer control-secret" {
			t.Error("settings edit left authenticated control route")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["principal_id"] != "alice" || body["environment"] != "dev" || body["base_revision"] != revision {
			t.Errorf("publisher request was not bound to the published revision: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"published_revision": "r-new", "changed": true})
	}))
	defer server.Close()
	backend := api.AgentExecBackend{
		Exec: func(context.Context, api.EffectiveBinding, api.AgentExecRequest) (api.AgentExecResult, error) {
			t.Fatal("settings reached runtime executor")
			return api.AgentExecResult{}, nil
		},
		Settings: settingsBackend(root, "dev", server.URL, "control-secret"),
	}
	effective := api.EffectiveBinding{Binding: api.ToolBinding{PrincipalID: "alice"}, Definition: api.ToolDefinition{Transport: api.AgentTools}}
	read := api.ToolSpec{Name: "settings_read", Executor: "handler:settings"}
	badIdentity := effective
	badIdentity.Binding.PrincipalID = "../alice"
	if _, err := backend.Call(context.Background(), badIdentity, read, nil); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("unvalidated principal reached passport path: %v", err)
	}
	out, err := backend.Call(context.Background(), effective, read, map[string]any{"key": "compression.enabled"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.Structured.(map[string]any)
	if result["published_revision"] != revision || result["effective_revision"] != revision || result["published_values"].(map[string]any)["compression.enabled"] != true {
		t.Fatalf("settings read did not report published state: %v", result)
	}
	edit := api.ToolSpec{Name: "settings_edit", Executor: "handler:settings"}
	if _, err := backend.Call(context.Background(), effective, edit, map[string]any{"key": "compression.enabled", "value": "false"}); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("edit without base revision: %v", err)
	}
	out, err = backend.Call(context.Background(), effective, edit, map[string]any{"key": "compression.enabled", "value": "false", "base_revision": revision})
	if err != nil || out.Structured.(map[string]any)["published_revision"] != "r-new" || serverCalls.Load() != 1 {
		t.Fatalf("settings edit did not reach publisher once: %v %v", out, err)
	}
	journal := filepath.Join(root, "alice", "passports", "apply-journal.json")
	if err := os.WriteFile(journal, []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Call(context.Background(), effective, read, nil); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("unfinished apply was readable: %v", err)
	}
}
