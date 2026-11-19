package automation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/commops"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

func TestAutomationServiceSignatureBindsCallerAndPayload(t *testing.T) {
	ctx := t.Context()
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	host := h.host(t)
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.EnsureSigningKeys(ctx); err != nil {
		t.Fatal(err)
	}
	key := contract.AutomationServiceCredential("synthetic-control")
	issuer := httptest.NewServer(contract.AutomationServiceHandler(registry, key, host.cfg.Environment))
	defer issuer.Close()
	toolhub := httptest.NewServer(host.SignedControlHandler(registry))
	defer toolhub.Close()
	signer := contract.NewAutomationServiceSigner(issuer.URL, key)
	client := commops.ControlClient{URL: toolhub.URL, Environment: host.cfg.Environment, Signer: signer}
	var schedules []commodel.Schedule
	if err := client.Call(ctx, alice, "schedule.list", nil, &schedules); err != nil {
		t.Fatalf("signed service dispatch refused: %v", err)
	}
	raw, err := json.Marshal(map[string]any{"operation": "schedule.list", "caller": alice, "payload": nil})
	if err != nil {
		t.Fatal(err)
	}
	scope := contract.ServiceRequest{Space: alice.ContextID, Environment: host.cfg.Environment, RequestDigest: contract.PolicyDigest(raw)}
	if _, err := contract.NewAutomationServiceSigner(issuer.URL, "synthetic-control").IssueAutomationService(ctx, scope); err == nil {
		t.Fatal("shared control bearer minted a service assertion")
	}
	token, err := signer.IssueAutomationService(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body      []byte
		token     string
		principal string
	}{
		{raw, "synthetic-control", alice.PrincipalID},
		{append(bytes.Clone(raw), ' '), token, ""},
		{raw, token, "bob"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/automation/operations", bytes.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+tc.token)
		req.Header.Set("X-Hub-Principal", tc.principal)
		w := httptest.NewRecorder()
		host.SignedControlHandler(registry).ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("unsafe service request reached dispatch: status=%d", w.Code)
		}
	}
}
