package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestSkillRefLeaseRoutesOnlyMatchingRuntime(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/skill-ref-lease" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("lease sent to wrong runtime")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"GITHUB_TOKEN": "caller-value"})
	}))
	defer upstream.Close()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	normalized, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(normalized)] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", PolicyVersion: "policy-1", Address: upstream.URL, State: Ready}, binding: normalized, auth: "secret", leases: map[string]Lease{}}
	m.mu.Unlock()
	call := func(principal, policy string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-ref-lease", strings.NewReader(`{"principal_id":"`+principal+`","context_id":"alice","runtime_id":"alice","policy_version":"`+policy+`","refs":["GITHUB_TOKEN"]}`))
		req.Header.Set("Authorization", "Bearer secret")
		recorder := httptest.NewRecorder()
		m.Handler().ServeHTTP(recorder, req)
		return recorder
	}
	unauthorized := httptest.NewRecorder()
	m.Handler().ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/skill-ref-lease", strings.NewReader(`{}`)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	if rec := call("bob", "policy-1"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("foreign principal status=%d", rec.Code)
	}
	if rec := call("alice", "old-policy"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale policy status=%d", rec.Code)
	}
	if rec := call("alice", "policy-1"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "caller-value") {
		t.Fatalf("owned lease status=%d", rec.Code)
	}
}

func TestSkillRefLeaseRoutesByAdmittedJobLane(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"lane": "gateway-2"})
	}))
	defer upstream.Close()
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	b := binding(root)
	b.RuntimeMode = "gateway-2"
	b, err := m.normalize(b)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway-2", PolicyVersion: "policy-1", Address: upstream.URL, State: Ready}, binding: b, auth: "secret", leases: map[string]Lease{}}
	m.jobs["job-2"] = jobRecord{Request: hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1")}, RuntimeMode: "gateway-2", Status: "running"}
	m.mu.Unlock()
	for _, tc := range []struct {
		job, principal string
		want           int
	}{
		{"job-2", "alice", http.StatusOK},
		{"job-2", "bob", http.StatusServiceUnavailable},
		{"missing", "alice", http.StatusServiceUnavailable},
	} {
		body, _ := json.Marshal(map[string]any{"principal_id": tc.principal, "context_id": "alice", "runtime_id": "alice", "policy_version": "policy-1", "job_id": tc.job, "refs": []string{"GITHUB_TOKEN"}})
		req := httptest.NewRequest(http.MethodPost, "/v1/skill-ref-lease", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("job=%s principal=%s status=%d, want %d", tc.job, tc.principal, rec.Code, tc.want)
		}
	}
}

func TestRuntimeFailureClassesAreAllowlisted(t *testing.T) {
	cases := map[string]string{"administrative hold": "hold", "runtime recovery backoff or crash budget exhausted": "backoff", "stale-policy runtime removal failed": "policy_rotation", "start runtime: secret=private": "spawn", "random secret=private": "unavailable"}
	for input, want := range cases {
		if got := runtimeFailureClass(errors.New(input)); got != want {
			t.Fatalf("class=%s want=%s", got, want)
		}
	}
	rec := httptest.NewRecorder()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	writeRuntimeFailure(rec, http.StatusServiceUnavailable, errors.New("start runtime: secret=private"))
	if strings.Contains(rec.Body.String(), "private") || !strings.Contains(rec.Body.String(), `"class":"spawn"`) {
		t.Fatalf("unsafe failure response: %s", rec.Body.String())
	}
	if strings.Contains(output.String(), "private") || !strings.Contains(output.String(), "stage=start_runtime reason=other") {
		t.Fatal("runtime failure log did not allowlist its stage")
	}
	output.Reset()
	writeRuntimeFailure(rec, http.StatusServiceUnavailable, errors.New("runtime is not registered: secret=private"))
	if strings.Contains(output.String(), "private") || !strings.Contains(output.String(), "reason=runtime_is_not_registered") {
		t.Fatal("runtime failure log did not allowlist its reason")
	}
}
