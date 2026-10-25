package toolhub

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	"github.com/letya999/hermes-hub/internal/credentialbroker"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func oauthStateFixture(t *testing.T, account string) (api.CredentialInjector, api.EffectiveBinding, string, *bool) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "session.json"), []byte(`{"token":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	checkpointed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease_1", GrantID: "grant_1", CredentialID: "credential_1"})
		case "/v1/runtime/leases/lease_1/materialize":
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{Env: map[string]string{"ACCOUNT": account}, Mounts: []brokerv1.Mount{{Source: state, Target: "/session"}}})
		case "/v1/runtime/leases/lease_1/release":
			var release brokerv1.RuntimeRelease
			if err := json.NewDecoder(r.Body).Decode(&release); err != nil {
				t.Error(err)
			}
			checkpointed = release.Checkpoint && release.Quiesced
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := credentialbroker.Config{URL: server.URL, KeyFile: keyPath, KeyID: "toolhub", Issuer: "hermes-toolhub"}
	effective := api.EffectiveBinding{Binding: api.ToolBinding{ToolBindingID: "binding_1", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1"}, Credential: &api.CredentialReference{Backend: "credential-broker", BrokerGrantID: "grant_1", Keys: []string{"ACCOUNT"}, BrokerEnv: map[string]string{"ACCOUNT": "ACCOUNT"}}, WorkloadID: "workload_1"}
	effective.Definition.Credentials = []api.CredentialInput{{Name: "ACCOUNT", Required: true, Delivery: "env"}}
	effective.Definition.OAuth = &api.OAuthRecipe{StateTarget: "/session", TokenFile: "session.json"}
	if err := json.Unmarshal([]byte(`{"account_input":"ACCOUNT"}`), effective.Definition.OAuth); err != nil {
		t.Fatal(err)
	}
	return brokerRuntimeInjector(&cfg, &cfg), effective, state, &checkpointed
}

func TestOAuthAccountStateRejectsOversize(t *testing.T) {
	inject, effective, dir, checkpointed := oauthStateFixture(t, "fixture@example.test")
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"token":"`+strings.Repeat("x", 65536)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if injection, err := inject(t.Context(), effective); err == nil {
		injection.Cleanup()
		t.Fatal("oversize session accepted")
	}
	if *checkpointed {
		t.Fatal("rejected state checkpointed")
	}
}

func TestOAuthAccountStatePersistsRefresh(t *testing.T) {
	inject, effective, dir, checkpointed := oauthStateFixture(t, "fixture+work@example.test")
	injection, err := inject(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	defer injection.Cleanup()
	alias := filepath.Join(dir, "fixture%2Bwork@example.test.json")
	data, err := os.ReadFile(alias)
	if err != nil || string(data) != `{"token":"old"}` {
		t.Fatal("account session view absent", err)
	}
	if err := os.WriteFile(alias, []byte(`{"token":"refreshed"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := injection.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil || string(data) != `{"token":"refreshed"}` || !*checkpointed {
		t.Fatal("refreshed state lost", err)
	}
}

func TestOAuthAccountStateRejectsTraversal(t *testing.T) {
	inject, effective, _, checkpointed := oauthStateFixture(t, "../foreign@example.test")
	if injection, err := inject(t.Context(), effective); err == nil {
		injection.Cleanup()
		t.Fatal("unsafe account accepted")
	}
	if *checkpointed {
		t.Fatal("rejected state checkpointed")
	}
}

func TestOAuthAccountStateRejectsSymlink(t *testing.T) {
	inject, effective, dir, checkpointed := oauthStateFixture(t, "fixture@example.test")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"private":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "fixture@example.test.json")); err != nil {
		t.Skip("symlink unavailable", err)
	}
	if injection, err := inject(t.Context(), effective); err == nil {
		injection.Cleanup()
		t.Fatal("symlink session accepted")
	}
	if *checkpointed {
		t.Fatal("rejected state checkpointed")
	}
}

func TestOAuthAccountStateRejectsInvalidJSON(t *testing.T) {
	inject, effective, dir, _ := oauthStateFixture(t, "fixture@example.test")
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if injection, err := inject(t.Context(), effective); err == nil {
		injection.Cleanup()
		t.Fatal("invalid session accepted")
	}
}

func TestOAuthAccountStateRequiresDeclaredMount(t *testing.T) {
	inject, effective, _, _ := oauthStateFixture(t, "fixture@example.test")
	effective.Definition.OAuth.StateTarget = "/different"
	if injection, err := inject(t.Context(), effective); err == nil {
		injection.Cleanup()
		t.Fatal("wrong session mount accepted")
	}
}

func TestOAuthAccountStateCallbackStartsEmpty(t *testing.T) {
	inject, effective, dir, checkpointed := oauthStateFixture(t, "fixture@example.test")
	if err := os.Remove(filepath.Join(dir, "session.json")); err != nil {
		t.Fatal(err)
	}
	injection, err := inject(t.Context(), effective)
	if err != nil {
		t.Fatal(err)
	}
	defer injection.Cleanup()
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"token":"first"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := injection.Checkpoint(); err != nil || !*checkpointed {
		t.Fatal("initial callback session not persisted", err)
	}
}
