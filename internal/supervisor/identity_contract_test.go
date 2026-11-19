package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/contracts/identity/v1/conformance"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

func TestControlHostsIdentityContract(t *testing.T) {
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.ImportLegacy(context.Background(), "fixture", "personal", []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}}); err != nil {
		t.Fatal(err)
	}
	conformance.Both(t, r, func(token string) http.Handler {
		return (&Manager{cfg: Config{RuntimeAuth: token, Identity: r}}).Handler()
	}, contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}, contract.Principal{ID: "alice", Kind: contract.Human})
}

func TestServiceIssuanceRejectsSharedControlCredential(t *testing.T) {
	registry, _ := fixtureIdentity(t)
	if err := registry.EnsureSigningKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	const control, issuer = "synthetic-shared-control", "independent-service-issuer"
	m := &Manager{cfg: Config{Identity: registry, RuntimeAuth: control, CommunicationServiceAuth: issuer, Environment: "dev"}}
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	scope := contract.ServiceRequest{Space: "alice", Environment: "dev", RequestDigest: contract.PolicyDigest([]byte("synthetic request"))}
	for _, bootstrap := range []string{control, contract.AutomationServiceCredential(control), contract.MemoryCaptureServiceCredential(control), contract.MemoryCaptureServiceCredential(issuer)} {
		client := contract.NewAutomationServiceSigner(server.URL, bootstrap)
		if _, err := client.IssueAutomationService(t.Context(), scope); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("invalid bootstrap reached automation issuance: %v", err)
		}
	}
	automation := contract.NewAutomationServiceSigner(server.URL, contract.AutomationServiceCredential(issuer))
	if _, err := automation.IssueAutomationService(t.Context(), scope); err != nil {
		t.Fatalf("independent automation issuer rejected: %v", err)
	}
	capture := contract.NewMemoryCaptureServiceSigner(server.URL, contract.MemoryCaptureServiceCredential(issuer))
	if _, err := capture.IssueMemoryCaptureService(t.Context(), scope); err != nil {
		t.Fatalf("independent capture issuer rejected: %v", err)
	}
}

func TestDreamingModelIssuanceRequiresAnActiveAllowedPassport(t *testing.T) {
	registry, _ := fixtureIdentity(t)
	ctx := t.Context()
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	publish := func(previous, rev string, enabled bool) string {
		t.Helper()
		bundle := contract.PolicyBundle{Space: "alice", Environment: "dev", PassportRevision: rev, Passports: map[string]json.RawMessage{}}
		for _, name := range []string{"stack", "config", "models", "workspace", "tools", "grants"} {
			bundle.Passports[name] = json.RawMessage(fmt.Sprintf(`{"kind":%q,"metadata":{"space":"alice","environment":"dev","revision":%q},"memory":{"enabled":%t},"allow":{"models":["use"],"memory":["dreaming"]}}`, name, rev, enabled))
		}
		policy, err := registry.StagePassports(ctx, bundle)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.ActivatePassports(ctx, "alice", "dev", previous, policy.Digest); err != nil {
			t.Fatal(err)
		}
		return policy.Digest
	}
	digest := publish("", "r-1", true)
	const control, communication, dreaming = "mock-control", "mock-communication", "mock-dreaming"
	m := &Manager{cfg: Config{Identity: registry, RuntimeAuth: control, CommunicationServiceAuth: communication, DreamingServiceAuth: dreaming, Environment: "dev"}}
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	scope := contract.ServiceRequest{Space: "alice", Environment: "dev", PolicyRevision: digest, RequestDigest: contract.PolicyDigest([]byte("fixture"))}
	for _, bootstrap := range []string{control, communication, contract.MemoryCaptureServiceCredential(communication)} {
		if _, err := contract.NewDreamingModelSigner(server.URL, bootstrap).IssueDreamingModelService(ctx, scope); !errors.Is(err, contract.ErrForbidden) {
			t.Fatalf("another control credential reached dreaming issuance: %v", err)
		}
	}
	client := contract.NewDreamingModelSigner(server.URL, dreaming)
	proof, err := client.IssueDreamingModelService(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := registry.SigningKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if audience, err := contract.VerifyModelServiceToken(proof, scope, keys, time.Now()); err != nil || audience != contract.DreamingModelAudience {
		t.Fatal("issuer did not sign the requested dreaming authority")
	}
	next := publish(digest, "r-2", false)
	if _, err := client.IssueDreamingModelService(ctx, scope); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("obsolete passport reached dreaming issuance: %v", err)
	}
	scope.PolicyRevision = next
	if _, err := client.IssueDreamingModelService(ctx, scope); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("disabled memory reached dreaming issuance: %v", err)
	}
}

func TestControlIdentityRouteWithoutRegistryFailsClosed(t *testing.T) {
	m := &Manager{cfg: Config{RuntimeAuth: "identity-token"}}
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	if _, err := contract.NewHTTPClient(srv.URL, "identity-token", srv.Client()).Resolve(context.Background(), contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatalf("missing registry: %v", err)
	}
}

func TestControlBindFailureDoesNotOpenRegistry(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dir := t.TempDir()
	m := &Manager{cfg: Config{StateDir: dir, Environment: "dev"}}
	if err := Serve(context.Background(), m, listener.Addr().String()); err == nil {
		t.Fatal("occupied address accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatal("failed listener created registry state")
	}
}

func TestControlOwnsRegistryUntilShutdown(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	m.cfg.Identity = nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, m, "127.0.0.1:0") }()
	var client contract.Client
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for client == nil {
		select {
		case err := <-done:
			t.Fatalf("control stopped during startup: %v", err)
		case <-deadline:
			t.Fatal("registry never became available")
		case <-ticker.C:
			client = m.identityClient()
		}
	}
	if _, err := client.Principal(ctx, "absent"); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("open registry: %v", err)
	}
	if keys, err := client.SigningKeys(ctx); err != nil || len(keys) != 4 {
		t.Fatalf("control did not initialize all signing purposes: count=%d err=%v", len(keys), err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("control did not stop")
	}
	if m.identityClient() != nil {
		t.Fatal("stopped control retained registry")
	}
	if _, err := client.Principal(context.Background(), "absent"); !errors.Is(err, contract.ErrUnavailable) {
		t.Fatalf("registry not closed: %v", err)
	}
}

func TestControlDoesNotCloseInjectedRegistry(t *testing.T) {
	r, _ := fixtureIdentity(t)
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, nil)
	m.cfg.Identity = r
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, m, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Principal(context.Background(), "alice"); err != nil {
		t.Fatalf("injected registry was closed: %v", err)
	}
}

func fixtureIdentity(t *testing.T) (*identityhub.Registry, []identityhub.Mapping) {
	t.Helper()
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	mappings, err := r.ImportLegacy(context.Background(), "fixture", "personal", []commodel.User{{ID: "alice", Enabled: true, TelegramIDs: []int64{7}}})
	if err != nil {
		t.Fatal(err)
	}
	return r, mappings
}

func TestControlIdentityProofNeedsSeparateIngressCredential(t *testing.T) {
	r, _ := fixtureIdentity(t)
	m := &Manager{cfg: Config{RuntimeAuth: "service-control", IdentityLinkAuth: "channel-ingress", Identity: r}}
	server := httptest.NewServer(m.Handler())
	defer server.Close()
	proof := contract.OIDCLink{Channel: contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}, PrincipalID: "alice", Issuer: "https://issuer.example", Subject: "subject"}
	if err := contract.NewLinkHTTPClient(server.URL, "service-control", server.Client()).VerifyOIDC(context.Background(), proof); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("service credential asserted proof: %v", err)
	}
	links, err := r.Bindings(context.Background(), "alice")
	if err != nil || links[0].Issuer != "" {
		t.Fatal("rejected request changed proof")
	}
	if err := contract.NewLinkHTTPClient(server.URL, "channel-ingress", server.Client()).VerifyOIDC(context.Background(), proof); err != nil {
		t.Fatal(err)
	}
}

func TestControlDeactivatesHumanOnlyWithDistinctAdminCredential(t *testing.T) {
	registry, mappings := fixtureIdentity(t)
	m := &Manager{cfg: Config{RuntimeAuth: "read", IdentityLinkAuth: "ingress", AdminAuth: "admin", Environment: "dev", Identity: registry}}
	handler := m.Handler()
	deactivate := func(token, origin, environment string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/identity/deactivate", strings.NewReader(`{"principal_id":"alice","environment":"`+environment+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		req.Header.Set("X-Hub-Principal", "bob")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	for _, token := range []string{"", "read", "ingress"} {
		if got := deactivate(token, "", "dev"); got != http.StatusForbidden {
			t.Fatalf("deactivation without admin authority returned %d", got)
		}
	}
	if got := deactivate("admin", "https://browser.example", "dev"); got != http.StatusForbidden {
		t.Fatalf("browser-origin deactivation returned %d", got)
	}
	if got := deactivate("admin", "", "prod"); got != http.StatusBadRequest {
		t.Fatalf("cross-environment deactivation returned %d", got)
	}
	ctx := context.Background()
	channel := contract.ChannelIdentity{Channel: contract.Telegram, ID: "7"}
	if _, err := registry.Resolve(ctx, channel); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := deactivate("admin", "", "dev"); got != http.StatusOK {
			t.Fatalf("deactivation or retry returned %d", got)
		}
	}
	if _, err := registry.Resolve(ctx, channel); !errors.Is(err, contract.ErrNotFound) {
		t.Fatalf("retired channel still resolves: %v", err)
	}
	if _, err := registry.Template(ctx, contract.TemplateRequest{Subject: "alice", Actor: mappings[0].Agent, Space: "alice"}); !errors.Is(err, contract.ErrForbidden) {
		t.Fatalf("retired agent still acts: %v", err)
	}
}

func TestControlSigningKeyRotationRequiresDistinctAdminCredential(t *testing.T) {
	r, _ := fixtureIdentity(t)
	if err := r.EnsureSigningKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: Config{RuntimeAuth: "read", IdentityLinkAuth: "ingress", AdminAuth: "admin", Environment: "dev", Identity: r}}
	handler := m.Handler()
	for _, tc := range []struct {
		token, origin, environment string
		status                     int
	}{
		{"read", "", "dev", http.StatusForbidden},
		{"ingress", "", "dev", http.StatusForbidden},
		{"admin", "https://browser.example", "dev", http.StatusForbidden},
		{"admin", "", "prod", http.StatusBadRequest},
		{"admin", "", "dev", http.StatusNoContent},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/admin/identity/keys/rotate", strings.NewReader(`{"environment":"`+tc.environment+`"}`))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("rotation status=%d want=%d", w.Code, tc.status)
		}
	}
	keys, err := r.SigningKeys(t.Context())
	if err != nil || len(keys) != 8 {
		t.Fatalf("expected one successful rotation: count=%d err=%v", len(keys), err)
	}
}
