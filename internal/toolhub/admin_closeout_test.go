package toolhub

import (
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/http"
	"strings"
	"testing"
)

func TestAdminAuditFailureDeniesDiagnostics(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v1/admin/onboardings", ""},
		{"GET", "/v1/admin/diagnose/ob-1", ""},
		{"POST", "/v1/admin/inspect-source", `{"onboarding_id":"ob-1"}`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			g, h := adminFixture(t, nil)
			g.AuditWrite = func(string, map[string]string) error { return errors.New("ledger unavailable") }
			w := adminCall(h, "admin-bearer-token-0000000000000000", tc.method, tc.path, tc.body)
			if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "alice") {
				t.Fatalf("unaudited cross-principal read: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestAdminOnboardingDoesNotEchoProviderText(t *testing.T) {
	view := adminOnboardingOf(api.Onboarding{OnboardingID: "ob-1", PrincipalID: "alice", Phase: api.PhaseFailed, Error: "opaque-provider-bearer", ErrorCode: "opaque-code-bearer", Required: []api.CredentialHint{{Name: "API_TOKEN"}}, Finalizers: []api.Finalizer{{Name: api.FinalizerWorkloads, State: api.FinalizerFailed, Attempts: 2, Error: "opaque-finalizer-bearer"}}})
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"opaque-provider-bearer", "opaque-code-bearer", "opaque-finalizer-bearer"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("allowlisted diagnostic DTO echoed untrusted provider text: %s", secret)
		}
	}
	for _, metadata := range []string{"alice", "ob-1", "failed", "API_TOKEN", "workloads"} {
		if !strings.Contains(string(body), metadata) {
			t.Errorf("safe diagnostic metadata missing: %s", metadata)
		}
	}
}
