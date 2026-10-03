package mediasvc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	brokerv1 "github.com/letya999/credential-broker/api/v1"

	"github.com/letya999/hermes-hub/internal/credentialbroker"
)

// fakeMediaBroker serves acquire/materialize/release over TLS like Credential
// Broker. acquired/released count lease lifecycle calls.
func fakeMediaBroker(t *testing.T, env map[string]string, acquired, released *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/leases", func(w http.ResponseWriter, r *http.Request) {
		var in brokerv1.AcquireLease
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.GrantID == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		acquired.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Lease{ID: "lease-1", GrantID: in.GrantID, CredentialID: "cred-1", Revision: 1, ExpiresAt: time.Now().Add(time.Minute)})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/materialize", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(brokerv1.Materialized{LeaseID: "lease-1", ExpiresAt: time.Now().Add(time.Minute), Env: env})
	})
	mux.HandleFunc("POST /v1/runtime/leases/lease-1/release", func(w http.ResponseWriter, r *http.Request) {
		released.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func mediaBrokerConfig(t *testing.T, srv *httptest.Server) credentialbroker.Config {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "media.private")
	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	return credentialbroker.Config{URL: srv.URL, KeyFile: keyPath, KeyID: "media", Issuer: "hermes-media", HTTPClient: srv.Client()}
}

func brokeredMediaConfig(t *testing.T, srv *httptest.Server) Config {
	t.Helper()
	return Config{
		Role: RoleMedia, Auth: "tok", Engine: "fal", BrokerGrant: "grant-media-1",
		Broker:      mediaBrokerConfig(t, srv),
		PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice",
		PolicyVersion: "policy-1",
	}
}

func TestMediaBrokerKeyResolver(t *testing.T) {
	acquired, released := &atomic.Int32{}, &atomic.Int32{}
	srv := fakeMediaBroker(t, map[string]string{"HUB_MEDIA_UPSTREAM_KEY": "resolved-key"}, acquired, released)
	resolve, err := brokeredMediaConfig(t, srv).mediaKeyResolver()
	if err != nil {
		t.Fatal(err)
	}
	key, err := resolve(context.Background())
	if err != nil || key != "resolved-key" {
		t.Fatalf("resolve: %v %q", err, key)
	}
	if acquired.Load() != 1 || released.Load() != 1 {
		t.Fatalf("lease lifecycle: acquired=%d released=%d", acquired.Load(), released.Load())
	}
	// Second call inside the cache TTL must not re-acquire a lease.
	if key, err = resolve(context.Background()); err != nil || key != "resolved-key" {
		t.Fatalf("cached resolve: %v %q", err, key)
	}
	if acquired.Load() != 1 {
		t.Fatal("short-TTL cache did not suppress a second acquire")
	}
}

func TestMediaBrokerResolverErrors(t *testing.T) {
	// A grant without broker client env fails closed at construction.
	if _, err := (Config{Role: RoleMedia, Auth: "x", Engine: "fal", BrokerGrant: "g", PrincipalID: "a"}).mediaKeyResolver(); err == nil {
		t.Fatal("grant without broker env accepted")
	}
	// Materialization without a provider key env fails and still releases.
	acquired, released := &atomic.Int32{}, &atomic.Int32{}
	srv := fakeMediaBroker(t, map[string]string{"UNRELATED": "x"}, acquired, released)
	resolve, err := brokeredMediaConfig(t, srv).mediaKeyResolver()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = resolve(context.Background()); err == nil {
		t.Fatal("empty materialization produced a key")
	}
	if released.Load() != 1 {
		t.Fatal("lease not released on materialize failure")
	}
	// Errors are never cached: the next call re-acquires.
	if _, err = resolve(context.Background()); err == nil {
		t.Fatal("second resolve should also fail")
	}
	if acquired.Load() != 2 {
		t.Fatal("error was cached")
	}
	// Alternative env names materialize too (FAL_KEY delivery).
	srv2 := fakeMediaBroker(t, map[string]string{"FAL_KEY": "fal-resolved"}, &atomic.Int32{}, &atomic.Int32{})
	resolve2, err := brokeredMediaConfig(t, srv2).mediaKeyResolver()
	if err != nil {
		t.Fatal(err)
	}
	if key, err := resolve2(context.Background()); err != nil || key != "fal-resolved" {
		t.Fatalf("FAL_KEY materialization: %v %q", err, key)
	}
}

func TestMediaBrokerEngineWiring(t *testing.T) {
	// No grant configured: resolver is nil and engines keep env keys.
	resolve, err := (Config{Role: RoleMedia, Auth: "x"}).mediaKeyResolver()
	if err != nil || resolve != nil {
		t.Fatalf("unexpected resolver: %v", err)
	}
	acquired, released := &atomic.Int32{}, &atomic.Int32{}
	srv := fakeMediaBroker(t, map[string]string{"FAL_KEY": "broker-fal"}, acquired, released)
	cfg := brokeredMediaConfig(t, srv)
	cfg.Upstream = "https://fal.run"
	eng, err := genEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fal, ok := eng.(*falGen)
	if !ok {
		t.Fatalf("engine: %#v", eng)
	}
	key, err := fal.apiKey(context.Background())
	if err != nil || key != "broker-fal" {
		t.Fatalf("engine broker key: %v %q", err, key)
	}
	if !fal.Ready(context.Background()) {
		t.Fatal("brokered engine reported not ready")
	}
	// ConfigFromEnv wires the grant and broker client env together.
	for _, kv := range [][2]string{
		{"HUB_MEDIA_AUTH", "tok"}, {"HUB_MEDIA_ENGINE", "fal"}, {"HUB_MEDIA_IMAGE_MODEL", "fal-ai/flux-2/klein/9b"},
		{"HUB_MEDIA_BROKER_GRANT", "grant-media-1"},
		{"HUB_CREDENTIAL_BROKER_MEDIA_URL", srv.URL}, {"HUB_CREDENTIAL_BROKER_MEDIA_KEY_FILE", cfg.Broker.KeyFile},
		{"HUB_CREDENTIAL_BROKER_MEDIA_KEY_ID", "media"}, {"HUB_CREDENTIAL_BROKER_MEDIA_ISSUER", "hermes-media"},
		{"HUB_PRINCIPAL_ID", "alice"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	parsed, err := ConfigFromEnv(RoleMedia)
	if err != nil || parsed.BrokerGrant != "grant-media-1" || !parsed.Broker.Enabled() {
		t.Fatalf("env config: %v %+v", err, parsed.Broker)
	}
	if _, err := parsed.mediaKeyResolver(); err != nil {
		t.Fatal(err)
	}
}

func TestCachedKeyExpiry(t *testing.T) {
	calls := 0
	resolve := cachedKey(func(context.Context) (string, error) {
		calls++
		return "k", nil
	}, time.Nanosecond)
	if _, err := resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("expired cache did not re-resolve")
	}
	failing := cachedKey(func(context.Context) (string, error) { return "", errors.New("down") }, time.Hour)
	if _, err := failing(context.Background()); err == nil {
		t.Fatal("resolver error swallowed")
	}
}
