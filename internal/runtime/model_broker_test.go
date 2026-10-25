package runtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"
	brokeridentity "github.com/letya999/credential-broker/identity"
)

func TestModelRelayBrokerCredentialIsOwnerBoundAndLeaseReleased(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var acquired, materialized, released, providerCalls atomic.Int32
	var materializeFails atomic.Bool
	var targetMissing atomic.Bool
	var releaseFails atomic.Bool
	var acquireFails atomic.Bool
	verifier := brokeridentity.Verifier{Keys: map[string]brokeridentity.TrustedKey{"model": {PublicKey: public, Issuer: "hermes-model", Audiences: []string{"broker:control", "broker:runtime"}}}}
	broker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		audience := "broker:runtime"
		if r.URL.Path == "/v1/leases" {
			audience = "broker:control"
		}
		actor, err := verifier.Verify(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), audience, r.Method, r.URL.RequestURI(), body)
		if err != nil || actor.PrincipalID != "alice" || actor.ContextID != "alice" || actor.RuntimeID != "alice" || actor.BindingID != "model-relay-p" || actor.WorkloadID != "model-relay" {
			http.Error(w, "wrong actor", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/leases":
			var request brokerv1.AcquireLease
			if json.Unmarshal(body, &request) != nil || request.GrantID != "grant-alice" {
				http.Error(w, "wrong grant", http.StatusForbidden)
				return
			}
			acquired.Add(1)
			if acquireFails.Load() {
				http.Error(w, `{"code":"unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease-1", GrantID: "grant-alice", ExpiresAt: time.Now().Add(time.Minute)})
		case "/v1/runtime/leases/lease-1/materialize":
			materialized.Add(1)
			if materializeFails.Load() {
				http.Error(w, `{"code":"unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			env := map[string]string{"OPENAI_API_KEY": "broker-provider-fixture"}
			if targetMissing.Load() {
				env = map[string]string{"OTHER_KEY": "wrong-target-fixture"}
			}
			_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease-1", Env: env})
		case "/v1/runtime/leases/lease-1/release":
			released.Add(1)
			if releaseFails.Load() {
				http.Error(w, `{"code":"unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer broker.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer broker-provider-fixture" {
			http.Error(w, "missing broker credential", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer provider.Close()
	dir := t.TempDir()
	key := filepath.Join(dir, "model.private")
	ca := filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(key, private, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: broker.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"URL": broker.URL, "KEY_FILE": key, "KEY_ID": "model", "ISSUER": "hermes-model", "CA_FILE": ca} {
		t.Setenv("HUB_CREDENTIAL_BROKER_MODEL_"+name, value)
	}
	file := filepath.Join(dir, "models.yaml")
	content := fmt.Sprintf("providers:\n  p: {url: '%s', allow_private: true, broker_grants: {alice: grant-alice}, credential_env: OPENAI_API_KEY}\nmodels:\n  default:\n    ladder: [{provider: p, model: reviewed}]\n", provider.URL)
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []string{"alice", "bob"} {
		t.Run(owner, func(t *testing.T) {
			t.Setenv("HUB_MODEL_RELAY_AUTH", owner+"-fixture")
			handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, owner+".json"), owner, "")
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
			req.Header.Set("Authorization", "Bearer "+owner+"-fixture")
			req.Header.Set("Content-Type", "application/json")
			before := providerCalls.Load()
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if owner == "alice" {
				if out.Code != 200 {
					t.Errorf("broker credential unavailable: status=%d", out.Code)
				}
			} else if out.Code != 403 || providerCalls.Load() != before {
				t.Errorf("foreign owner admitted: status=%d provider-calls=%d", out.Code, providerCalls.Load()-before)
			}
		})
	}
	t.Run("failed-materialization-releases-lease", func(t *testing.T) {
		materializeFails.Store(true)
		t.Setenv("HUB_MODEL_RELAY_AUTH", "alice-fixture")
		handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "failed.json"), "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
		req.Header.Set("Authorization", "Bearer alice-fixture")
		req.Header.Set("Content-Type", "application/json")
		providersBefore, acquiredBefore, releasedBefore := providerCalls.Load(), acquired.Load(), released.Load()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 502 || providerCalls.Load() != providersBefore || acquired.Load() != acquiredBefore+1 || released.Load() != releasedBefore+1 {
			t.Errorf("failed materialization leaked a lease or dispatched a provider: status=%d acquired-delta=%d released-delta=%d provider-delta=%d", out.Code, acquired.Load()-acquiredBefore, released.Load()-releasedBefore, providerCalls.Load()-providersBefore)
		}
	})
	t.Run("missing-target-rejects-with-release", func(t *testing.T) {
		materializeFails.Store(false)
		targetMissing.Store(true)
		t.Setenv("HUB_MODEL_RELAY_AUTH", "alice-fixture")
		handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "missing.json"), "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
		req.Header.Set("Authorization", "Bearer alice-fixture")
		req.Header.Set("Content-Type", "application/json")
		providersBefore, acquiredBefore, releasedBefore := providerCalls.Load(), acquired.Load(), released.Load()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 502 || providerCalls.Load() != providersBefore || acquired.Load() != acquiredBefore+1 || released.Load() != releasedBefore+1 {
			t.Errorf("missing credential target did not fail closed: status=%d acquired-delta=%d released-delta=%d provider-delta=%d", out.Code, acquired.Load()-acquiredBefore, released.Load()-releasedBefore, providerCalls.Load()-providersBefore)
		}
	})
	t.Run("failed-release-prevents-provider-dispatch", func(t *testing.T) {
		targetMissing.Store(false)
		releaseFails.Store(true)
		t.Setenv("HUB_MODEL_RELAY_AUTH", "alice-fixture")
		handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "release.json"), "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
		req.Header.Set("Authorization", "Bearer alice-fixture")
		req.Header.Set("Content-Type", "application/json")
		providersBefore, acquiredBefore, releasedBefore := providerCalls.Load(), acquired.Load(), released.Load()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 502 || providerCalls.Load() != providersBefore || acquired.Load() != acquiredBefore+1 || released.Load() != releasedBefore+1 {
			t.Errorf("failed release did not stop credential use: status=%d acquired-delta=%d released-delta=%d provider-delta=%d", out.Code, acquired.Load()-acquiredBefore, released.Load()-releasedBefore, providerCalls.Load()-providersBefore)
		}
	})
	t.Run("failed-acquire-prevents-provider-dispatch", func(t *testing.T) {
		releaseFails.Store(false)
		acquireFails.Store(true)
		t.Setenv("HUB_MODEL_RELAY_AUTH", "alice-fixture")
		handler, err := modelRelayRegistryHandler(file, filepath.Join(dir, "acquire.json"), "alice", "")
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"default"}`))
		req.Header.Set("Authorization", "Bearer alice-fixture")
		req.Header.Set("Content-Type", "application/json")
		providersBefore, acquiredBefore, releasedBefore := providerCalls.Load(), acquired.Load(), released.Load()
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 502 || providerCalls.Load() != providersBefore || acquired.Load() != acquiredBefore+1 || released.Load() != releasedBefore {
			t.Errorf("failed acquire did not fail closed: status=%d acquired-delta=%d released-delta=%d provider-delta=%d", out.Code, acquired.Load()-acquiredBefore, released.Load()-releasedBefore, providerCalls.Load()-providersBefore)
		}
	})
	if acquired.Load() != 5 || materialized.Load() != 4 || released.Load() != 4 {
		t.Errorf("lease lifecycle differs: acquired=%d materialized=%d released=%d", acquired.Load(), materialized.Load(), released.Load())
	}
}
