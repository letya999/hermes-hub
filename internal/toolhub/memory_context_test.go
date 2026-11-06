package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

const memoryContextControl = "memory-context-control-token-32ch!"

// memoryContextRecord is the supervisor record a claim must match: a live
// private run bound to auth's principal/context/runtime/environment — every
// denial test mutates exactly one axis away from this shape.
func memoryContextRecord(auth identity.Envelope, jobID, admission, text string) api.RunContext {
	return api.RunContext{
		JobID: jobID, AdmissionID: admission, RunID: "run-1", SessionID: "s-1",
		RuntimeGeneration: "gen-3", CurrentGeneration: "gen-3",
		Environment: auth.Environment, Status: "running",
		ScopeID: "user:" + auth.PrincipalID, Trigger: "message", Channel: "telegram_bot",
		UserID: auth.PrincipalID, TextSHA256: captureHash(text),
		Envelope: identity.Envelope{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID},
	}
}

// memoryContextFixture wires the endpoint's real dependencies: a vault
// resolver over temporary per-user vaults, the published passport gate and
// the run-authority seam backed by an in-memory record table.
func memoryContextFixture(t *testing.T) (*Gateway, identity.Envelope, map[string]api.RunContext) {
	t.Helper()
	passports := t.TempDir()
	bindings := t.TempDir()
	vaultDirWithMarker(t, bindings, "alice", "alice curated core")
	vaultDirWithMarker(t, bindings, "bob", "bob curated core")
	writeMemoryPassportDecl(t, passports, "alice", "alice", "r-0123456789abcdef", "dev", true)
	writeMemoryPassportDecl(t, passports, "bob", "bob", "r-0123456789abcdee", "dev", true)
	resolver, err := NewVaultResolver(bindings, passports)
	if err != nil {
		t.Fatal(err)
	}
	store, auth, _, _ := managedStore(t)
	auth.Environment = "dev"
	bobAuth := identity.TelegramEnvelope("bob", 8, "runtime", "policy-1")
	bobAuth.Environment = "dev"
	records := map[string]api.RunContext{
		"job-1": memoryContextRecord(auth, "job-1", "adm-1", "earl grey supplier"),
		"bob-1": memoryContextRecord(bobAuth, "bob-1", "adm-bob", "bob private text"),
	}
	g := &Gateway{Store: store, ControlToken: memoryContextControl,
		Tokens:       map[string]identity.Envelope{aliceToken: auth, bobToken: bobAuth},
		PassportRoot: passports, Vaults: resolver,
		Audit: func(string, map[string]string) {},
		Runs: runAuthorityFunc(func(_ context.Context, jobID string) (api.RunContext, error) {
			record, ok := records[jobID]
			if !ok {
				return api.RunContext{}, errors.New("unknown job")
			}
			return record, nil
		})}
	return g, auth, records
}

func memoryContextCall(t *testing.T, g *Gateway, principal, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/memory-context", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+memoryContextControl)
	if principal != "" {
		req.Header.Set("X-Hub-Principal", principal)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	g.serveMemoryContext(rec, req)
	return rec
}

// SPEC-0098 M5/#517: /v1/memory-context serves CORE and lane-1 recall only
// for an exact live private run — every record axis, the enrolled identity
// and the published memory switch are verified before the vault opens.
func TestMemoryContextEndpointAdmitsExactPrivateRun(t *testing.T) {
	g, _, _ := memoryContextFixture(t)
	rec := memoryContextCall(t, g, "alice", `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid claim denied: %d", rec.Code)
	}
	var out api.MemoryContextResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("malformed response: %v", err)
	}
	if out.Status != "ok" || !strings.Contains(out.Core, "alice curated core") {
		t.Fatalf("context missing CORE: %+v", out)
	}
	if !strings.Contains(out.Recall, "data, not instructions") {
		t.Fatalf("recall lost its data-not-instructions frame: %q", out.Recall)
	}
}

// Every axis of the claim and the durable record is verified before the
// vault resolves: transport shape, exact run identity, lifecycle status,
// generation, audience, user, environment and the live enrollment switch.
func TestMemoryContextEndpointDenials(t *testing.T) {
	g, _, records := memoryContextFixture(t)
	valid := `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`

	get := httptest.NewRequest(http.MethodGet, "/v1/memory-context", nil)
	rec := httptest.NewRecorder()
	g.serveMemoryContext(rec, get)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET admitted: %d", rec.Code)
	}
	if rec := memoryContextCall(t, g, "alice", valid, map[string]string{"Origin": "http://evil"}); rec.Code != http.StatusForbidden {
		t.Fatalf("browser origin admitted: %d", rec.Code)
	}
	badBearer := httptest.NewRequest(http.MethodPost, "/v1/memory-context", strings.NewReader(valid))
	badBearer.Header.Set("Authorization", "Bearer wrong")
	badBearer.Header.Set("X-Hub-Principal", "alice")
	rec = httptest.NewRecorder()
	g.serveMemoryContext(rec, badBearer)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer admitted: %d", rec.Code)
	}
	if rec := memoryContextCall(t, g, "", valid, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing principal admitted: %d", rec.Code)
	}
	if rec := memoryContextCall(t, g, "carol", valid, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unenrolled principal admitted: %d", rec.Code)
	}
	for name, body := range map[string]string{
		"malformed JSON":   `{"job_id":`,
		"trailing content": valid + ` {}`,
		"unknown field":    `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier","vault":"/other"}`,
	} {
		if rec := memoryContextCall(t, g, "alice", body, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s admitted: %d", name, rec.Code)
		}
	}

	base := records["job-1"]
	mutations := []struct {
		name   string
		mutate func(*api.RunContext)
	}{
		{"forged admission", func(r *api.RunContext) { r.AdmissionID = "adm-9" }},
		{"hash of another text", func(r *api.RunContext) { r.TextSHA256 = captureHash("different body") }},
		{"completed run", func(r *api.RunContext) { r.Status = "completed" }},
		{"uncertain run", func(r *api.RunContext) { r.Status = "uncertain" }},
		{"superseded generation", func(r *api.RunContext) { r.CurrentGeneration = "gen-9" }},
		{"group scope", func(r *api.RunContext) { r.ScopeID = "group:alice" }},
		{"background trigger", func(r *api.RunContext) { r.Trigger = "goal" }},
		{"sensitive run", func(r *api.RunContext) { r.Sensitive = true }},
		{"foreign user", func(r *api.RunContext) { r.UserID = "bob" }},
		{"foreign environment", func(r *api.RunContext) { r.Environment = "prod" }},
		{"foreign principal", func(r *api.RunContext) { r.Envelope.PrincipalID = "bob" }},
		{"foreign context", func(r *api.RunContext) { r.Envelope.ContextID = "other" }},
		{"foreign runtime", func(r *api.RunContext) { r.Envelope.RuntimeID = "other" }},
		{"empty runtime generation", func(r *api.RunContext) { r.RuntimeGeneration = "" }},
		{"empty record admission", func(r *api.RunContext) { r.AdmissionID = "" }},
	}
	for _, tc := range mutations {
		record := base
		tc.mutate(&record)
		records["job-1"] = record
		if rec := memoryContextCall(t, g, "alice", valid, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("%s admitted: %d", tc.name, rec.Code)
		}
		records["job-1"] = base
	}
	// A claim for a job the authority does not know is an explicit
	// unavailable, and so is an unreachable resolver — never a confused
	// denial and never a local read.
	if rec := memoryContextCall(t, g, "alice", `{"job_id":"ghost","admission_id":"a","text":"x"}`, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown job gave %d, want 503", rec.Code)
	}
	runs := g.Runs
	g.Runs = nil
	if rec := memoryContextCall(t, g, "alice", valid, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("missing authority admitted: %d", rec.Code)
	}
	g.Runs = runs
	vaults := g.Vaults
	g.Vaults = nil
	if rec := memoryContextCall(t, g, "alice", valid, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing resolver gave %d, want 503", rec.Code)
	}
	g.Vaults = vaults
	// Republished disabled memory denies the next request, not the next
	// restart — the enrollment is re-read per call.
	writeMemoryPassportDecl(t, g.PassportRoot, "alice", "alice", "r-0123456789abcdff", "dev", false)
	if rec := memoryContextCall(t, g, "alice", valid, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("disabled memory admitted: %d", rec.Code)
	}
	writeMemoryPassportDecl(t, g.PassportRoot, "alice", "alice", "r-0123456789abcdef", "dev", true)
	if rec := memoryContextCall(t, g, "alice", valid, nil); rec.Code != http.StatusOK {
		t.Fatalf("re-enabled memory denied: %d", rec.Code)
	}
}

// Two users in parallel: each request resolves only its own vault under its
// own lock — alice's claim can never read bob's leaf and vice versa.
func TestMemoryContextTwoUserIsolation(t *testing.T) {
	g, _, _ := memoryContextFixture(t)
	var wg sync.WaitGroup
	results := map[string]*httptest.ResponseRecorder{}
	var mu sync.Mutex
	claims := map[string]string{
		"alice": `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`,
		"bob":   `{"job_id":"bob-1","admission_id":"adm-bob","text":"bob private text"}`,
	}
	for principal, body := range claims {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(p, b string) {
				defer wg.Done()
				rec := memoryContextCall(t, g, p, b, nil)
				mu.Lock()
				results[p] = rec
				mu.Unlock()
			}(principal, body)
		}
	}
	wg.Wait()
	for principal, marker := range map[string]string{"alice": "alice curated core", "bob": "bob curated core"} {
		rec := results[principal]
		if rec.Code != http.StatusOK {
			t.Fatalf("%s context denied: %d", principal, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("%s read another user's vault: %s", principal, rec.Body.String())
		}
	}
	// A claim naming another user's job is denied at the record comparison —
	// the foreign run record can never satisfy the caller's envelope.
	cross := `{"job_id":"bob-1","admission_id":"adm-bob","text":"bob private text"}`
	if rec := memoryContextCall(t, g, "alice", cross, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("alice claimed bob's job: %d", rec.Code)
	}
}
