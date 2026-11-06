package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	g, auth, records, _ := memoryContextFixtureWithGrants(t)
	return g, auth, records
}

func memoryContextFixtureWithGrants(t *testing.T) (*Gateway, identity.Envelope, map[string]api.RunContext, *GovernanceStore) {
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
	store, auth, policy, profile := managedStore(t)
	store.RightsDoc.Capabilities["memory"] = api.RightCapability{Description: "fixture", Operations: map[string]api.RightOperation{
		"read":   {Subject: "self", Scope: "path", Issuers: []string{"operator"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"},
		"search": {Subject: "self", Scope: "path", Issuers: []string{"operator"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"},
	}}
	policy.Revision++
	policy.RightCeiling = append(policy.RightCeiling, api.RightRule{Capability: "memory", Operation: "read"})
	if err := putPolicy(t, store, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	profile.Rights = append(profile.Rights, api.RightRule{Capability: "memory", Operation: "read"}, api.RightRule{Capability: "memory", Operation: "search"})
	if err := putProfile(t, store, profile); err != nil {
		t.Fatal(err)
	}
	gov := NewGovernanceStore(filepath.Join(t.TempDir(), "governance.json"))
	store.RightsLedger = gov.Load
	for _, user := range []string{"alice", "bob"} {
		for _, op := range []string{"read", "search"} {
			mintRight(t, gov, store.RightsDoc, api.NewRightGrant("context-"+user+"-"+op, user, "memory", op, "", "fixture", "operator", time.Now().Add(time.Hour)))
		}
	}
	auth.Environment = "dev"
	bobAuth := identity.TelegramEnvelope("bob", 8, "runtime", "policy-1")
	bobAuth.CapabilityProfile, bobAuth.Environment, bobAuth.Generation, bobAuth.Organization = "bob-default", "dev", 1, "example"
	bobProfile := profile
	bobProfile.ProfileID, bobProfile.PrincipalID, bobProfile.ContextID, bobProfile.RuntimeID = bobAuth.CapabilityProfile, bobAuth.PrincipalID, bobAuth.ContextID, bobAuth.RuntimeID
	bobProfile.Revision = 1
	bobProfile.Selections = nil // system recall does not require model tool selection
	if err := putProfile(t, store, bobProfile); err != nil {
		t.Fatal(err)
	}
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
	return g, auth, records, gov
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

func TestAuditMemoryContextRequiresReadAuthority(t *testing.T) {
	const claim = `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`
	check := func(t *testing.T, g *Gateway, want int) {
		t.Helper()
		rec := memoryContextCall(t, g, "alice", claim, nil)
		if rec.Code != want || (want != http.StatusOK && strings.Contains(rec.Body.String(), "alice curated core")) {
			t.Fatalf("memory context status=%d body=%q, want %d without private CORE", rec.Code, rec.Body.String(), want)
		}
	}
	for _, op := range []string{"read", "search"} {
		t.Run("revoked-"+op, func(t *testing.T) {
			g, _, _, gov := memoryContextFixtureWithGrants(t)
			check(t, g, http.StatusOK)
			if err := gov.RevokeRight("context-alice-"+op, "operator", "fixture revoke", time.Now()); err != nil {
				t.Fatal(err)
			}
			check(t, g, http.StatusForbidden)
		})
	}
	t.Run("missing", func(t *testing.T) {
		g, _, _, _ := memoryContextFixtureWithGrants(t)
		g.Store.RightsLedger = func() (*api.Governance, error) { return &api.Governance{}, nil }
		check(t, g, http.StatusForbidden)
	})
	t.Run("pdp-unavailable", func(t *testing.T) {
		g, _, _, _ := memoryContextFixtureWithGrants(t)
		g.Store.RightsLedger = func() (*api.Governance, error) { return nil, errors.New("unavailable") }
		check(t, g, http.StatusServiceUnavailable)
	})
	for _, tc := range []struct{ op, scope, want string }{
		{"read", "memory/cards", "wrong-read-scope"},
		{"search", "memory/cards", "narrow-search-scope"},
		{"read", "memory/CORE.md", "exact-core-scope"},
		{"search", "memory", "full-search-scope"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			g, _, _, gov := memoryContextFixtureWithGrants(t)
			if err := gov.RevokeRight("context-alice-"+tc.op, "operator", "replace with scoped grant", time.Now()); err != nil {
				t.Fatal(err)
			}
			mintRight(t, gov, g.Store.RightsDoc, api.NewRightGrant("scoped-"+tc.op, "alice", "memory", tc.op, tc.scope, "fixture", "operator", time.Now().Add(time.Hour)))
			want := http.StatusOK
			if tc.want == "wrong-read-scope" || tc.want == "narrow-search-scope" {
				want = http.StatusForbidden
			}
			check(t, g, want)
		})
	}
}

func TestMemoryContextRechecksRightsAtVaultRead(t *testing.T) {
	g, _, _, gov := memoryContextFixtureWithGrants(t)
	u, err := g.Vaults.Resolve("alice", "dev")
	if err != nil {
		t.Fatal(err)
	}
	held, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- u.Locked(func(string) error { close(held); <-release; return nil }) }()
	<-held
	prior := g.Store.RightsLedger
	checked := make(chan struct{})
	var reads atomic.Int32
	g.Store.RightsLedger = func() (*api.Governance, error) {
		ledger, err := prior()
		if reads.Add(1) == 2 {
			close(checked)
		}
		return ledger, err
	}
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response <- memoryContextCall(t, g, "alice", `{"job_id":"job-1","admission_id":"adm-1","text":"earl grey supplier"}`, nil)
	}()
	select {
	case <-checked:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("rights preflight did not complete")
	}
	if err := gov.RevokeRight("context-alice-read", "operator", "revoke before vault read", time.Now()); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	rec := <-response
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "alice curated core") {
		t.Fatalf("revoked before vault read returned %d %q", rec.Code, rec.Body.String())
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
