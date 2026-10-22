package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// adminFixture builds a gateway with an admin-scoped bearer, a plain
// principal bearer and one onboarding owned by alice.
func adminFixture(t *testing.T, audits *[]string) (*Gateway, http.Handler) {
	t.Helper()
	store, auth, _ := seededStore(t)
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: "ob-1", PrincipalID: "alice",
		ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1",
		Mode: api.OnboardingSelfInstall, Phase: api.PhaseFailed,
		Error: "probe failed", ErrorCode: "probe_failed",
		SourceURL: "https://github.com/example/mcp", CommitSHA: strings.Repeat("a", 40),
		DefinitionID: "google-work", DefinitionVersion: "1.0.0",
		Required:   []api.CredentialHint{{Name: "API_TOKEN", Secret: true, Delivery: "env"}},
		Finalizers: []api.Finalizer{{Name: "workload", State: "failed", Attempts: 2, Error: "rm timeout"}},
		// Capability-bearing fields are seeded so a non-whitelisted DTO
		// would visibly leak them.
		ConfirmationNonce: "live-nonce-value",
		WizardNonce:       "live-wizard-value",
		Revision:          1, CreatedAt: time.Now().UTC(),
	}
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatal(err)
	}
	admin := identity.MockEnvelope("hermes-admin", 1, "runtime-admin", "policy-1")
	admin.Scopes = []string{"admin"}
	g := &Gateway{
		Store:   store,
		Backend: api.RoutingBackend{MCP: stubBackend{text: "unused"}},
		Tokens: map[string]identity.Envelope{
			"admin-bearer-token-0000000000000000": admin,
			"alice-principal-token-0000000000000": auth,
		},
	}
	if audits != nil {
		g.Audit = func(event string, fields map[string]string) {
			*audits = append(*audits, event+":"+fields["admin"]+":"+fields["target"])
		}
	}
	handler, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	return g, handler
}

func adminCall(handler http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func TestAdminRateLimited(t *testing.T) {
	_, handler := adminFixture(t, nil)
	old := adminMinInterval
	adminMinInterval = 120 * time.Millisecond
	defer func() { adminMinInterval = old }()
	start := time.Now()
	for i := 0; i < 3; i++ {
		w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/onboardings", "")
		if w.Code != http.StatusOK {
			t.Fatalf("call %d: %d", i, w.Code)
		}
	}
	if elapsed := time.Since(start); elapsed < 2*adminMinInterval {
		t.Fatalf("admin calls not serialized+paced: %v", elapsed)
	}
	// The same gate applies to diagnose.
	start = time.Now()
	for i := 0; i < 2; i++ {
		w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/diagnose/ob-1", "")
		if w.Code != http.StatusOK {
			t.Fatalf("diagnose %d: %d", i, w.Code)
		}
	}
	if elapsed := time.Since(start); elapsed < adminMinInterval {
		t.Fatalf("diagnose calls not paced: %v", elapsed)
	}
}

func TestAdminDiagnoseCrossPrincipal(t *testing.T) {
	_, handler := adminFixture(t, nil)
	w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/diagnose/ob-1", "")
	if w.Code != http.StatusOK {
		t.Fatalf("diagnose: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"ob-1", "alice", "failed", "probe failed", "probe_failed", "google-work", "API_TOKEN", "https://github.com/example/mcp", strings.Repeat("a", 40)} {
		if !strings.Contains(body, want) {
			t.Fatalf("diagnose missing %q: %s", want, body)
		}
	}
	// Capability-bearing fields must never serialize to the admin surface.
	for _, banned := range []string{"confirmation_nonce", "wizard_nonce", "remove_nonce", "device_code", "live-nonce-value", "live-wizard-value"} {
		if strings.Contains(body, banned) {
			t.Fatalf("diagnose leaked capability field %q: %s", banned, body)
		}
	}
	// Finalizer detail is the forensic payload.
	if !strings.Contains(body, "workload") || !strings.Contains(body, "rm timeout") {
		t.Fatalf("finalizers missing: %s", body)
	}
	if w := adminCall(handler, "alice-principal-token-0000000000000", http.MethodGet, "/v1/admin/diagnose/ob-1", ""); w.Code != http.StatusForbidden {
		t.Fatalf("owner principal reached diagnose: %d", w.Code)
	}
	if w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/diagnose/nope", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

func TestAdminAuditTagged(t *testing.T) {
	var audits []string
	_, handler := adminFixture(t, &audits)
	old := adminMinInterval
	adminMinInterval = time.Millisecond
	defer func() { adminMinInterval = old }()
	adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/onboardings", "")
	adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/diagnose/ob-1", "")
	if len(audits) != 2 {
		t.Fatalf("audit events: %v", audits)
	}
	if audits[0] != "admin.diagnostics:hermes-admin:onboardings" {
		t.Fatalf("audit[0]: %q", audits[0])
	}
	if audits[1] != "admin.diagnostics:hermes-admin:ob-1" {
		t.Fatalf("audit[1]: %q", audits[1])
	}
}

func TestAdminInspectSource(t *testing.T) {
	old := adminMinInterval
	adminMinInterval = time.Millisecond
	defer func() { adminMinInterval = old }()
	oldFetch := adminFetchSource
	defer func() { adminFetchSource = oldFetch }()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "server.go", Mode: 0644, Size: int64(len("package main // uses API_TOKEN\n"))})
	_, _ = tw.Write([]byte("package main // uses API_TOKEN\n"))
	_ = tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0644, Size: int64(len("# example mcp\n"))})
	_, _ = tw.Write([]byte("# example mcp\n"))
	_ = tw.Close()
	var gotSource api.ArtifactSource
	adminFetchSource = func(ctx context.Context, source api.ArtifactSource, maxBytes int64) ([]byte, error) {
		gotSource = source
		return buf.Bytes(), nil
	}
	_, handler := adminFixture(t, nil)
	w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodPost, "/v1/admin/inspect-source", `{"onboarding_id":"ob-1","grep":"API_TOKEN"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	for _, want := range []string{"server.go", "README.md", "API_TOKEN"} {
		if !strings.Contains(body, want) {
			t.Fatalf("inspect missing %q: %s", want, body)
		}
	}
	if gotSource.Repository != "https://github.com/example/mcp" || gotSource.CommitSHA != strings.Repeat("a", 40) {
		t.Fatalf("fetched wrong source: %+v", gotSource)
	}
	// Whole file bodies must not ship back — only bounded hits.
	if strings.Contains(body, "package main") {
		t.Fatalf("inspect leaked file body: %s", body)
	}
	for _, denied := range [][]any{
		{"alice-principal-token-0000000000000", http.StatusForbidden},
		{"", http.StatusUnauthorized},
	} {
		token, wantCode := denied[0].(string), denied[1].(int)
		if w := adminCall(handler, token, http.MethodPost, "/v1/admin/inspect-source", `{"onboarding_id":"ob-1"}`); w.Code != wantCode {
			t.Fatalf("token %q inspect: %d, want %d", token, w.Code, wantCode)
		}
	}
	if w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodPost, "/v1/admin/inspect-source", `{"onboarding_id":"nope"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown onboarding: %d", w.Code)
	}
	if w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/inspect-source", ""); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET inspect: %d", w.Code)
	}
}

func TestAdminInspectSourceValidation(t *testing.T) {
	old := adminMinInterval
	adminMinInterval = time.Millisecond
	defer func() { adminMinInterval = old }()
	calls := 0
	oldFetch := adminFetchSource
	adminFetchSource = func(ctx context.Context, source api.ArtifactSource, maxBytes int64) ([]byte, error) {
		calls++
		return nil, errors.New("upstream unreachable")
	}
	defer func() { adminFetchSource = oldFetch }()
	_, handler := adminFixture(t, nil)
	for _, body := range []string{
		`{"onboarding_id":"ob-1",`,
		`{"onboarding_id":"ob-1","extra":true}`,
		`{"onboarding_id":""}`,
		`{}`,
		`{"onboarding_id":"../x"}`,
		`{"onboarding_id":"ob-1","grep":"a\nb"}`,
	} {
		if w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodPost, "/v1/admin/inspect-source", body); w.Code != http.StatusBadRequest {
			t.Fatalf("body %q: %d, want 400", body, w.Code)
		}
	}
	if calls != 0 {
		t.Fatalf("rejected bodies still fetched: %d", calls)
	}
	if w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodPost, "/v1/admin/inspect-source", `{"onboarding_id":"ob-1"}`); w.Code != http.StatusBadGateway {
		t.Fatalf("fetch failure: %d, want 502", w.Code)
	}
	if calls != 1 {
		t.Fatalf("fetch calls: %d", calls)
	}
}

func TestAdminInspectSourceBounded(t *testing.T) {
	old := adminMinInterval
	adminMinInterval = time.Millisecond
	defer func() { adminMinInterval = old }()
	oldFetch := adminFetchSource
	defer func() { adminFetchSource = oldFetch }()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < 200; i++ {
		body := "hit " + strings.Repeat("x", 8) + "\n"
		name := "f" + strings.Repeat("0", 4-len(strconv.Itoa(i))) + strconv.Itoa(i) + ".go"
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	adminFetchSource = func(ctx context.Context, source api.ArtifactSource, maxBytes int64) ([]byte, error) {
		return buf.Bytes(), nil
	}
	_, handler := adminFixture(t, nil)
	w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodPost, "/v1/admin/inspect-source", `{"onboarding_id":"ob-1","grep":"hit"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("inspect: %d", w.Code)
	}
	var out struct {
		Files []string          `json:"files"`
		Hits  []adminInspectHit `json:"hits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) > 64 {
		t.Fatalf("unbounded hits: %d", len(out.Hits))
	}
}

func TestAdminRequiresScope(t *testing.T) {
	_, handler := adminFixture(t, nil)
	if w := adminCall(handler, "alice-principal-token-0000000000000", http.MethodGet, "/v1/admin/onboardings", ""); w.Code != http.StatusForbidden {
		t.Fatalf("plain principal reached admin surface: %d", w.Code)
	}
	if w := adminCall(handler, "", http.MethodGet, "/v1/admin/onboardings", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous reached admin surface: %d", w.Code)
	}
	w := adminCall(handler, "admin-bearer-token-0000000000000000", http.MethodGet, "/v1/admin/onboardings", "")
	if w.Code != http.StatusOK {
		t.Fatalf("admin onboardings: %d %s", w.Code, w.Body)
	}
	var out struct {
		Onboardings []map[string]any `json:"onboardings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Onboardings) != 1 || out.Onboardings[0]["onboarding_id"] != "ob-1" {
		t.Fatalf("list: %s", w.Body)
	}
}
