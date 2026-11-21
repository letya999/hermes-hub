package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
)

// captureFixture stands up the real production boundary for #516: a genuine
// ToolHub endpoint handler behind httptest with the rendered per-user vault
// bindings, published six-passports, the supervisor lifecycle authority and
// the communication control-plane bearer — and a real communication Gateway
// whose spool is the durable capture producer.
type captureFixture struct {
	t           *testing.T
	server      *httptest.Server
	records     map[string]api.RunContext
	vaults      string
	spoolDir    string
	config      Config
	supervisor  *httptest.Server
	toolhubEnv  map[string]string
	store       *toolhub.Store
	gov         *toolhub.GovernanceStore
	doc         api.RightsDoc
	spawnClient spawnv1.Client
}

// captureNilBackend is a fail-closed agent backend: the capture path never
// dispatches model tools, so any call reaching it is a routing bug.
type captureNilBackend struct{ t *testing.T }

func (b captureNilBackend) Call(context.Context, api.EffectiveBinding, api.ToolSpec, map[string]any) (api.BackendResult, error) {
	b.t.Error("capture fixture reached the agent backend")
	return api.BackendResult{}, api.ErrIsolation
}

// newCaptureFixture wires a supervised communication gateway (owner alice)
// to a real ToolHub endpoint. Supervisor records start empty; tests seed the
// lifecycle facts they need.
func newCaptureFixture(t *testing.T) *captureFixture {
	t.Helper()
	f := &captureFixture{t: t, records: map[string]api.RunContext{}, vaults: t.TempDir()}

	store := toolhub.NewStore()
	doc := api.RightsDoc{Release: "fixture", DefinitionID: "hub-agent-rights", Capabilities: map[string]api.RightCapability{
		"tools":  {Operations: map[string]api.RightOperation{"use": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "read", Description: "fixture"}}},
		"memory": {Operations: map[string]api.RightOperation{"remember": {Subject: "self", Scope: "none", Issuers: []string{"platform"}, PEP: []string{"test"}, Effect: "write", Description: "fixture"}}},
	}}
	store.RightsDoc = doc
	govPath := filepath.Join(t.TempDir(), "governance.json")
	gov := toolhub.NewGovernanceStore(govPath)
	store.RightsLedger = gov.Load
	spawnSrv := httptest.NewServer(spawnv1.Handler(spawnv1.NewInProc(&fake.Server{}), "spawn-token"))
	t.Cleanup(spawnSrv.Close)
	f.spawnClient = spawnv1.NewHTTPClient(spawnSrv.URL, "spawn-token", spawnSrv.Client())
	f.toolhubEnv = map[string]string{
		"HUB_VAULTS_BY_PRINCIPAL":    f.vaults,
		"HUB_COMMUNICATION_AUTH":     "comm-token",
		"HUB_TOOL_GOVERNANCE":        govPath,
		"HUB_AUDIT_LEDGER":           filepath.Join(t.TempDir(), "audit.jsonl"),
		"HUB_RUNTIME_SUPERVISOR_URL": "",
		"HUB_SPAWN_URL":              spawnSrv.URL,
		"HUB_SPAWN_TOKEN":            "spawn-token",
	}
	f.supervisor = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer comm-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/holds" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
		record, ok := f.records[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(record)
	}))
	t.Cleanup(f.supervisor.Close)
	f.toolhubEnv["HUB_RUNTIME_SUPERVISOR_URL"] = f.supervisor.URL

	f.store, f.gov, f.doc = store, gov, doc
	return f
}

// enroll registers a principal in the ToolHub token table, rights ledger and
// published passports, and renders its real vault root under the bindings
// dir — the same server-owned records production renders.
func (f *captureFixture) enroll(user string, memoryEnabled, rememberRight bool) identity.Envelope {
	f.t.Helper()
	env := identity.TelegramEnvelope(user, 7, "runtime", "policy-1")
	env.CapabilityProfile, env.Environment, env.Generation = user+"-default", "dev", 1
	rights := []api.RightRule{{Capability: "tools", Operation: "use"}, {Capability: "memory", Operation: "remember"}}
	policy := api.CapabilityPolicy{Schema: api.SchemaVersion, PolicyID: user + "-policy", Members: []string{user}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "capture fixture", Status: api.ActiveStatus, RightCeiling: rights}
	if err := api.Confirm(&policy, policy.IssuedBy, time.Now()); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.PutCapabilityPolicy(policy); err != nil {
		f.t.Fatal(err)
	}
	profile := api.CapabilityProfile{Schema: api.SchemaVersion, ProfileID: env.CapabilityProfile, PrincipalID: user,
		ContextID: env.ContextID, RuntimeID: env.RuntimeID, Environment: "dev", Generation: 1, PolicyVersion: env.PolicyVersion,
		PolicyID: policy.PolicyID, PolicyRevision: policy.Revision, Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt,
		Reason: "capture fixture", Status: api.ActiveStatus, Rights: rights}
	if err := api.Confirm(&profile, profile.IssuedBy, time.Now()); err != nil {
		f.t.Fatal(err)
	}
	if err := f.store.PutCapabilityProfile(profile); err != nil {
		f.t.Fatal(err)
	}
	writeCapturePassport(f.t, f.spawnClient, user, "dev", memoryEnabled)
	if rememberRight {
		right := api.NewRightGrant(api.DeterministicID("rightgrant", user, "memory", "remember"), user, "memory", "remember", "", "fixture", "platform", time.Now().Add(time.Hour))
		if err := api.Confirm(&right, right.GrantedBy, time.Now()); err != nil {
			f.t.Fatal(err)
		}
		if err := f.gov.GrantRight(right, f.doc, time.Now()); err != nil {
			f.t.Fatal(err)
		}
	}
	return env
}

func (f *captureFixture) mkVault(user string) string {
	f.t.Helper()
	dir := filepath.Join(f.vaults, user)
	if err := vault.Ensure(dir); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// serveToolHub starts (or restarts) the ToolHub endpoint against the same
// durable vaults/passports — the "restart the other side" half of the
// contract.
func (f *captureFixture) serveToolHub(t *testing.T, envelopes map[string]identity.Envelope) {
	t.Helper()
	for k, v := range f.toolhubEnv {
		t.Setenv(k, v)
	}
	aliceToken := strings.Repeat("a", 32)
	siblings := map[string]identity.Envelope{}
	for user, env := range envelopes {
		if user == "alice" {
			continue
		}
		token := strings.Repeat(string(user[0]), 32)
		siblings[token] = env
	}
	tokensFile := ""
	if len(siblings) > 0 {
		raw, err := json.Marshal(siblings)
		if err != nil {
			t.Fatal(err)
		}
		tokensFile = filepath.Join(t.TempDir(), "tokens.json")
		if err := os.WriteFile(tokensFile, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler, err := toolhub.NewEndpointHandler(toolhub.EndpointConfig{Token: aliceToken, Auth: envelopes["alice"], TokensFile: tokensFile,
		Backend: api.RoutingBackend{Agent: captureNilBackend{t}}}, f.store)
	if err != nil {
		t.Fatal(err)
	}
	if f.server != nil {
		f.server.Close()
	}
	f.server = httptest.NewServer(handler)
	t.Cleanup(f.server.Close)
}

// gateway builds a supervised communication gateway pointed at the live
// ToolHub endpoint; call it again on the same config to "restart" the
// producer over its durable spool.
func (f *captureFixture) gateway(t *testing.T, config Config) *Gateway {
	t.Helper()
	g, err := newTestGateway(t, config)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// leafEntries counts the durable capture marks inside one vault: daily-leaf
// mem:id blocks plus prov.db rows. Both must agree — a half-committed write
// would disagree.
func leafEntries(t *testing.T, vaultRoot string) (blocks int, provRows int) {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(vaultRoot, "memory", "daily", "*.md"))
	for _, leaf := range matches {
		raw, err := os.ReadFile(leaf)
		if err != nil {
			t.Fatal(err)
		}
		blocks += strings.Count(string(raw), "mem:id=")
	}
	prov, err := memory.OpenProvDB(filepath.Join(vaultRoot, "runtime", "memory"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return blocks, 0
		}
		t.Fatal(err)
	}
	entries, err := prov.Entries()
	if err != nil {
		t.Fatal(err)
	}
	return blocks, len(entries)
}

// leafBody returns the concatenated daily-leaf text for marker assertions —
// callers check the marker id/kind, never a full dump.
func leafBody(t *testing.T, vaultRoot string) string {
	t.Helper()
	var b strings.Builder
	matches, _ := filepath.Glob(filepath.Join(vaultRoot, "memory", "daily", "*.md"))
	for _, leaf := range matches {
		raw, err := os.ReadFile(leaf)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	return b.String()
}

// writeCapturePassport publishes the space's six-document revision through
// the spawn contract — the same submit→validate→activate path the enrolment
// check reads back via the Effective endpoint.
func writeCapturePassport(t *testing.T, client spawnv1.Client, user, environment string, enabled bool) {
	t.Helper()
	ctx := context.Background()
	revision := fmt.Sprintf("r-%d", time.Now().UnixNano())
	tenant := spawnv1.Tenant{Org: "local", Owner: user}
	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{Org: tenant.Org, Owner: tenant.Owner, ID: user, Environment: environment, IdempotencyKey: "mk-" + user}); err != nil && !errors.Is(err, spawnv1.ErrConflict) {
		t.Fatal(err)
	}
	docs := map[string]map[string]any{}
	for _, name := range spawnv1.Names {
		doc := map[string]any{
			"apiVersion": "six-passport/v2",
			"kind":       name,
			"metadata":   map[string]any{"space": user, "environment": environment, "revision": revision},
		}
		if name == "config" {
			doc["memory"] = map[string]any{"enabled": enabled}
		}
		docs[name] = doc
	}
	rev, err := client.SubmitRevision(ctx, user, "user", spawnv1.SubmitRevisionRequest{Org: tenant.Org, Owner: tenant.Owner, Documents: docs, IdempotencyKey: "sub-" + revision})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateRevision(ctx, user, rev.ID, spawnv1.RevisionRequest{Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: "val-" + revision}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ActivateRevisions(ctx, user, spawnv1.ActivationRequest{Org: tenant.Org, Owner: tenant.Owner, Revisions: []string{rev.ID}, IdempotencyKey: "act-" + revision}); err != nil {
		t.Fatal(err)
	}
}

// dropAfterCommit lets the POST reach ToolHub — the commit happens server
// side — and then eats the response, simulating the lost acknowledgement of
// the #516 contract.
type dropAfterCommit struct{ base http.RoundTripper }

type captureRunAPI struct{ *runAPI }

func (*captureRunAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (d dropAfterCommit) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := d.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil, errors.New("injected: acknowledgement lost after commit")
}

func captureJob(g *Gateway, userID, jobID string) Job {
	user := g.user(userID)
	return Job{Envelope: user.Envelope(11), ID: jobID, OrganizationID: "personal", UserID: userID, ActorID: userID,
		ScopeID: "user:" + userID, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: jobID + "-key",
		ChatID: 11, Text: "marker-" + jobID, CreatedAt: time.Now().UTC()}
}

func captureTestHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// TestCaptureRetryAfterRestartCommitsOnce is the #516 contract: a committed
// capture whose acknowledgement is lost, replayed after restarting BOTH the
// producer and ToolHub, lands exactly once — one leaf block, one prov.db row.
func TestCaptureRetryAfterRestartCommitsOnce(t *testing.T) {
	f := newCaptureFixture(t)
	aliceEnv := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	f.records["job-retry"] = api.RunContext{JobID: "job-retry", RunID: "run-retry-1", SessionID: "s-1", UserID: "alice",
		RuntimeGeneration: "gen-1", CurrentGeneration: "gen-1", Environment: "dev", Status: "completed",
		ScopeID: "user:alice", Trigger: "message", Channel: "telegram_bot", TextSHA256: captureTestHash("marker-job-retry"), ResponseSHA256: captureTestHash("answer marker"),
		Envelope: identity.Envelope{Schema: aliceEnv.Schema, PrincipalID: "alice", ContextID: aliceEnv.ContextID, RuntimeID: aliceEnv.RuntimeID, PolicyVersion: aliceEnv.PolicyVersion}}
	f.serveToolHub(t, map[string]identity.Envelope{"alice": aliceEnv})

	c := baseTestConfig(t)
	c.ToolHubURL = f.server.URL
	c.Supervised = true
	c.RuntimeURL = f.supervisor.URL
	c.RuntimeAuth = "comm-token"
	f.config, f.spoolDir = c, c.SpoolDir
	g := f.gateway(t, c)

	job := captureJob(g, "alice", "job-retry")
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	// The accepted turn is durable in the outbox before any transport.
	eventPath := filepath.Join(c.SpoolDir, "capture", "turn-job-retry.json")
	if _, err := os.Stat(eventPath); err != nil {
		t.Fatalf("accepted turn not persisted to the outbox: %v", err)
	}
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 0 || rows != 0 {
		t.Fatalf("vault written before transport ack: blocks=%d rows=%d", blocks, rows)
	}

	// Lost ack: ToolHub commits, the response never arrives — the event must
	// stay in the outbox for the retry.
	g.captureHTTP = &http.Client{Transport: dropAfterCommit{base: http.DefaultTransport}}
	g.pumpCaptures(context.Background())
	if _, err := os.Stat(eventPath); err != nil {
		t.Fatalf("lost-ack event dropped from the outbox: %v", err)
	}
	if blocks, _ := leafEntries(t, vaultRoot); blocks != 1 {
		t.Fatalf("commit did not land server-side: blocks=%d", blocks)
	}

	// Restart both sides over the same durable state: a fresh ToolHub
	// handler and a fresh gateway against the same spool dir. The pending
	// event reposts and ToolHub's durable dedup keeps it at one record.
	f.serveToolHub(t, map[string]identity.Envelope{"alice": aliceEnv})
	c.ToolHubURL = f.server.URL
	g2 := f.gateway(t, c)
	g2.pumpCaptures(context.Background())
	if _, err := os.Stat(eventPath); !os.IsNotExist(err) {
		t.Fatalf("acked event not cleared after restart retry: %v", err)
	}
	blocks, rows := leafEntries(t, vaultRoot)
	if blocks != 1 || rows != 1 {
		t.Fatalf("replay duplicated the capture: blocks=%d rows=%d", blocks, rows)
	}

	// The completed-run fact rides the same boundary: the reply event
	// verifies against the supervisor's durable lifecycle record and lands
	// once under prov=agent.
	outcome := commodel.RunOutcome{JobID: job.ID, RunID: "run-retry-1", SessionID: "s-1", Status: "completed", Text: "answer marker"}
	if err := g2.captureReply(job, outcome); err != nil {
		t.Fatal(err)
	}
	replyPath := g2.spool.capturePath("reply-" + job.ID)
	g2.captureHTTP = &http.Client{Transport: dropAfterCommit{base: http.DefaultTransport}}
	g2.pumpCaptures(context.Background())
	if _, err := os.Stat(replyPath); err != nil {
		t.Fatalf("reply lost-ack event dropped: %v", err)
	}
	f.serveToolHub(t, map[string]identity.Envelope{"alice": aliceEnv})
	c.ToolHubURL = f.server.URL
	g3 := f.gateway(t, c)
	g3.pumpCaptures(context.Background())
	if _, err := os.Stat(replyPath); !os.IsNotExist(err) {
		t.Fatalf("reply retry not settled: %v", err)
	}
	blocks, rows = leafEntries(t, vaultRoot)
	if blocks != 2 || rows != 2 {
		t.Fatalf("completed-run fact not captured once: blocks=%d rows=%d", blocks, rows)
	}
	body := leafBody(t, vaultRoot)
	if !strings.Contains(body, "prov=agent") || !strings.Contains(body, "answer marker") {
		t.Fatalf("reply entry missing agent provenance or body")
	}
}

// TestCapturePumpDrainsOnRun wires the drain loop into Gateway.Run: an
// accepted turn sitting in the outbox is posted by the loop itself — the
// production path never calls pumpCaptures by hand.
func TestCapturePumpDrainsOnRun(t *testing.T) {
	f := newCaptureFixture(t)
	aliceEnv := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	f.serveToolHub(t, map[string]identity.Envelope{"alice": aliceEnv})

	c := baseTestConfig(t)
	c.ToolHubURL = f.server.URL
	c.Supervised = true
	c.RuntimeURL = f.supervisor.URL
	c.RuntimeAuth = "comm-token"
	g := f.gateway(t, c)

	job := captureJob(g, "alice", "job-run-1")
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "running",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text), Envelope: aliceEnv}
	if err := g.spool.EnqueueCapture(captureEventForTurn(job)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.api = &captureRunAPI{runAPI: &runAPI{}}
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Fatalf("Run returned %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not stop after cancel")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if blocks, _ := leafEntries(t, vaultRoot); blocks == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("capture pump did not drain the accepted turn during Run")
}

func TestCaptureRetriesWhenRightsLedgerUnavailable(t *testing.T) {
	f := newCaptureFixture(t)
	alice := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	f.serveToolHub(t, map[string]identity.Envelope{"alice": alice})
	c := baseTestConfig(t)
	c.ToolHubURL, c.Supervised, c.RuntimeAuth, c.RuntimeURL = f.server.URL, true, "comm-token", f.supervisor.URL
	g := f.gateway(t, c)
	job := captureJob(g, "alice", "job-ledger-outage")
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "running",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text), Envelope: alice}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	f.store.RightsLedger = func() (*api.Governance, error) { return nil, errors.New("injected ledger outage") }
	g.pumpCaptures(context.Background())
	if _, err := os.Stat(g.spool.capturePath("turn-" + job.ID)); err != nil {
		t.Fatalf("ledger outage lost event: %v", err)
	}
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 0 || rows != 0 {
		t.Fatalf("ledger outage wrote vault: blocks=%d rows=%d", blocks, rows)
	}
	f.store.RightsLedger = f.gov.Load
	g.pumpCaptures(context.Background())
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 1 || rows != 1 {
		t.Fatalf("retry did not commit once: blocks=%d rows=%d", blocks, rows)
	}
}

func TestCaptureRejectsForeignForgedAndRevokedFacts(t *testing.T) {
	f := newCaptureFixture(t)
	alice := f.enroll("alice", true, true)
	bob := f.enroll("bob", true, true)
	aliceRoot, bobRoot := f.mkVault("alice"), f.mkVault("bob")
	f.serveToolHub(t, map[string]identity.Envelope{"alice": alice, "bob": bob})
	c := baseTestConfig(t)
	c.ToolHubURL, c.Supervised, c.RuntimeAuth = f.server.URL, true, "comm-token"
	c.RuntimeURL = f.supervisor.URL
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: t.TempDir(), WorkspaceDir: t.TempDir()})
	g := f.gateway(t, c)
	job := captureJob(g, "alice", "job-auth")
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "completed",
		RunID: "run-auth", ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel,
		TextSHA256: captureTestHash(job.Text), ResponseSHA256: captureTestHash("answer"), Envelope: alice}
	ev := captureEventForTurn(job)
	post := func(token, principal string, event api.CaptureEvent) int {
		t.Helper()
		status, err := g.postCapture(context.Background(), f.server.URL, token, principal, event)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	if code := post(strings.Repeat("a", 32), "alice", ev); code != http.StatusUnauthorized {
		t.Fatalf("model bearer minted fact: %d", code)
	}
	if code := post("comm-token", "bob", ev); code != http.StatusForbidden {
		t.Fatalf("foreign header minted fact: %d", code)
	}
	for name, change := range map[string]func(*api.CaptureEvent){
		"scope":       func(e *api.CaptureEvent) { e.ScopeID = "group:elsewhere" },
		"body":        func(e *api.CaptureEvent) { e.Body = "forged" },
		"provenance":  func(e *api.CaptureEvent) { e.Untrusted = true },
		"sensitivity": func(e *api.CaptureEvent) { e.Sensitive = true },
		"event_id":    func(e *api.CaptureEvent) { e.EventID = "turn-other" },
	} {
		bad := ev
		change(&bad)
		if code := post("comm-token", "alice", bad); code != http.StatusForbidden {
			t.Fatalf("%s forged fact admitted: %d", name, code)
		}
	}
	if blocks, _ := leafEntries(t, aliceRoot); blocks != 0 {
		t.Fatalf("forged fact wrote alice vault: %d", blocks)
	}
	if blocks, _ := leafEntries(t, bobRoot); blocks != 0 {
		t.Fatalf("foreign fact wrote bob vault: %d", blocks)
	}
	if err := f.gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "platform", "fixture revoke", time.Now()); err != nil {
		t.Fatal(err)
	}
	if code := post("comm-token", "alice", ev); code != http.StatusForbidden {
		t.Fatalf("revoked fact admitted: %d", code)
	}
	if blocks, _ := leafEntries(t, aliceRoot); blocks != 0 {
		t.Fatalf("revoked fact wrote vault: %d", blocks)
	}
	writeCapturePassport(t, f.spawnClient, "bob", "dev", false)
	bobJob := captureJob(g, "bob", "job-bob")
	f.records[bobJob.ID] = api.RunContext{JobID: bobJob.ID, UserID: "bob", Environment: "dev", Status: "running",
		ScopeID: bobJob.ScopeID, Trigger: bobJob.Trigger, Channel: bobJob.Channel,
		TextSHA256: captureTestHash(bobJob.Text), Envelope: bob}
	if code := post("comm-token", "bob", captureEventForTurn(bobJob)); code != http.StatusForbidden {
		t.Fatalf("disabled fact admitted: %d", code)
	}
	if blocks, _ := leafEntries(t, bobRoot); blocks != 0 {
		t.Fatalf("disabled fact wrote vault: %d", blocks)
	}
	writeCapturePassport(t, f.spawnClient, "bob", "dev", true)
	if code := post("comm-token", "bob", captureEventForTurn(bobJob)); code != http.StatusOK {
		t.Fatalf("bob's own capture denied: %d", code)
	}
	if blocks, rows := leafEntries(t, bobRoot); blocks != 1 || rows != 1 {
		t.Fatalf("bob capture missing from own vault: blocks=%d rows=%d", blocks, rows)
	}
	if blocks, _ := leafEntries(t, aliceRoot); blocks != 0 {
		t.Fatalf("bob capture reached alice vault: %d", blocks)
	}
	sensitive := captureJob(g, "alice", "job-sensitive")
	sensitive.Sensitive = true
	if _, err := g.spool.Enqueue(sensitive); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g.spool.capturePath("turn-" + sensitive.ID)); !os.IsNotExist(err) {
		t.Fatalf("sensitive turn entered capture outbox: %v", err)
	}
}

func TestManagedCaptureRequiresSignedCommunicationEvent(t *testing.T) {
	f := newCaptureFixture(t)
	auth := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	registry, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.Close() })
	if err := registry.EnsureSigningKeys(t.Context()); err != nil {
		t.Fatal(err)
	}
	const controlSecret, verifier, serviceSecret = "comm-token", "synthetic-read-verifier", "synthetic-communication-service"
	mux := http.NewServeMux()
	mux.Handle(contract.MemoryCaptureServiceAssertionPath, contract.MemoryCaptureServiceHandler(registry, contract.MemoryCaptureServiceCredential(serviceSecret), "dev"))
	mux.Handle("/v1/identity/", contract.Handler(registry, verifier))
	mux.HandleFunc("/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+verifier {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		record, ok := f.records[strings.TrimPrefix(r.URL.Path, "/v1/jobs/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(record)
	})
	control := httptest.NewServer(mux)
	defer control.Close()
	f.toolhubEnv["HUB_RUN_VERIFY_URL"], f.toolhubEnv["HUB_RUN_VERIFY_AUTH"] = control.URL, verifier
	f.serveToolHub(t, map[string]identity.Envelope{"alice": auth})
	cfg := baseTestConfig(t)
	cfg.ToolHubURL, cfg.Supervised, cfg.RuntimeURL, cfg.RuntimeAuth = f.server.URL, true, control.URL, controlSecret
	cfg.Environment = "dev"
	cfg.ServiceAuth = serviceSecret
	cfg.Runs = task.NewHTTPClient(control.URL, verifier, nil)
	g := f.gateway(t, cfg)
	job := captureJob(g, "alice", "job-signed-capture")
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "completed", RunID: "captured-run",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text), Envelope: auth}
	event := captureEventForTurn(job)
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	scope := contract.ServiceRequest{Space: "alice", Environment: "dev", RequestDigest: contract.PolicyDigest(body)}
	automationSigner := contract.NewMemoryCaptureServiceSigner(control.URL, contract.AutomationServiceCredential(serviceSecret))
	if _, err := automationSigner.IssueMemoryCaptureService(t.Context(), scope); err == nil {
		t.Fatal("automation bootstrap minted a capture permission")
	}
	token, err := registry.IssueMemoryCaptureService(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ bearer, principal, body string }{
		{controlSecret, "alice", string(body)},
		{token, "bob", string(body)},
		{token, "", string(body) + " "},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+"/v1/memory-capture", strings.NewReader(tc.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+tc.bearer)
		req.Header.Set("X-Hub-Principal", tc.principal)
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("invalid capture authority status=%d", res.StatusCode)
		}
	}
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 0 || rows != 0 {
		t.Fatalf("invalid capture wrote a vault entry: blocks=%d rows=%d", blocks, rows)
	}
	if status, err := g.postCapture(t.Context(), f.server.URL, controlSecret, "alice", event); err != nil || status != http.StatusOK {
		t.Fatalf("signed capture status=%d err=%v", status, err)
	}
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 1 || rows != 1 {
		t.Fatalf("signed capture did not commit: blocks=%d rows=%d", blocks, rows)
	}
}

func TestCompletedCaptureRecoveredFromTerminalReceipt(t *testing.T) {
	f := newCaptureFixture(t)
	alice := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	f.serveToolHub(t, map[string]identity.Envelope{"alice": alice})
	c := baseTestConfig(t)
	c.ToolHubURL, c.Supervised, c.RuntimeAuth, c.RuntimeURL = f.server.URL, true, "comm-token", f.supervisor.URL
	g := f.gateway(t, c)
	job := captureJob(g, "alice", "job-terminal-gap")
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	job.CaptureExpected = true // the worker reads the persisted job after enqueue
	outcome := RunOutcome{JobID: job.ID, RunID: "run-terminal", SessionID: "session-terminal", Status: "completed", Text: "answer terminal"}
	if err := g.spool.RecordTerminalOutcome(job, outcome, nil); err != nil {
		t.Fatal(err)
	}
	replyPath := g.spool.capturePath("reply-" + job.ID)
	if _, err := os.Stat(replyPath); err != nil {
		t.Fatalf("terminal receipt acknowledged before outbox: %v", err)
	}
	// Model the crash window after the terminal receipt but before capture.
	if err := g.spool.fsys.Remove(replyPath); err != nil {
		t.Fatal(err)
	}
	turnPath := g.spool.capturePath("turn-" + job.ID)
	if err := g.spool.fsys.Remove(turnPath); err != nil {
		t.Fatal(err)
	}
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", RunID: outcome.RunID, Environment: "dev", Status: "completed",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text),
		ResponseSHA256: captureTestHash(outcome.Text), Envelope: alice}
	g2 := f.gateway(t, c)
	if _, err := os.Stat(replyPath); err != nil {
		t.Fatalf("terminal receipt did not rebuild outbox: %v", err)
	}
	if _, err := os.Stat(turnPath); err != nil {
		t.Fatalf("accepted job did not rebuild outbox: %v", err)
	}
	g2.pumpCaptures(context.Background())
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 2 || rows != 2 {
		t.Fatalf("recovered fact count blocks=%d rows=%d", blocks, rows)
	}
}

func TestCompletedCaptureRecoveredBeforeTerminalApply(t *testing.T) {
	f := newCaptureFixture(t)
	alice := f.enroll("alice", true, true)
	vaultRoot := f.mkVault("alice")
	f.serveToolHub(t, map[string]identity.Envelope{"alice": alice})
	c := baseTestConfig(t)
	c.ToolHubURL, c.Supervised, c.RuntimeAuth, c.RuntimeURL = f.server.URL, true, "comm-token", f.supervisor.URL
	g := f.gateway(t, c)
	job := captureJob(g, "alice", "job-preapply-gap")
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	job.CaptureExpected = true
	outcome := RunOutcome{JobID: job.ID, RunID: "run-preapply", Status: "completed", Text: "answer preapply"}
	g.spool.onCompleted = func(Job, RunOutcome) error { return errors.New("injected crash before terminal apply") }
	if err := g.spool.RecordTerminalOutcome(job, outcome, nil); err == nil {
		t.Fatal("terminal apply unexpectedly succeeded")
	}
	f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", RunID: outcome.RunID, Environment: "dev", Status: "completed",
		ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text),
		ResponseSHA256: captureTestHash(outcome.Text), Envelope: alice}
	g2 := f.gateway(t, c)
	if err := g2.spool.recover(); err != nil {
		t.Fatal(err)
	}
	if err := g2.recoverCaptureOutbox(); err != nil {
		t.Fatal(err)
	}
	g2.pumpCaptures(context.Background())
	if blocks, rows := leafEntries(t, vaultRoot); blocks != 2 || rows != 2 {
		t.Fatalf("pre-apply recovery lost capture: blocks=%d rows=%d", blocks, rows)
	}
}

func TestCaptureAckRetentionFollowsJob(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	eventID, jobID := "turn-job-retention", "job-retention"
	if err := s.writeJSON(s.capturePath(eventID), api.CaptureEvent{EventID: eventID}); err != nil {
		t.Fatal(err)
	}
	if err := s.ackCapture(eventID); err != nil {
		t.Fatal(err)
	}
	marker := s.captureAckPath(eventID)
	at := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(marker, at, at); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, "pending", spoolFileID(jobID)+".json")
	if err := s.writeJSON(pending, Job{ID: jobID}); err != nil {
		t.Fatal(err)
	}
	compact := func() {
		t.Helper()
		if err := s.Compact(time.Now(), 24*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
			t.Fatal(err)
		}
	}
	compact()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("live job lost capture settlement: %v", err)
	}
	if err := s.fsys.Remove(pending); err != nil {
		t.Fatal(err)
	}
	compact()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("retired capture settlement not compacted: %v", err)
	}
}

func TestCaptureRechecksAuthorizationAtCommit(t *testing.T) {
	for _, mode := range []string{"revoke", "disable"} {
		t.Run(mode, func(t *testing.T) {
			f := newCaptureFixture(t)
			alice := f.enroll("alice", true, true)
			vaultRoot := f.mkVault("alice")
			f.serveToolHub(t, map[string]identity.Envelope{"alice": alice})
			c := baseTestConfig(t)
			c.ToolHubURL, c.Supervised, c.RuntimeAuth, c.RuntimeURL = f.server.URL, true, "comm-token", f.supervisor.URL
			g := f.gateway(t, c)
			job := captureJob(g, "alice", "job-barrier")
			f.records[job.ID] = api.RunContext{JobID: job.ID, UserID: "alice", Environment: "dev", Status: "running",
				ScopeID: job.ScopeID, Trigger: job.Trigger, Channel: job.Channel, TextSHA256: captureTestHash(job.Text), Envelope: alice}
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var reads atomic.Int32
			f.store.RightsLedger = func() (*api.Governance, error) {
				if reads.Add(1) == 2 {
					close(entered)
					<-release
				}
				return f.gov.Load()
			}
			result := make(chan int, 1)
			go func() {
				code, err := g.postCapture(context.Background(), f.server.URL, "comm-token", "alice", captureEventForTurn(job))
				if err != nil {
					result <- 0
					return
				}
				result <- code
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("capture did not reach the pre-commit right lookup")
			}
			if mode == "revoke" {
				if err := f.gov.RevokeRight(api.DeterministicID("rightgrant", "alice", "memory", "remember"), "platform", "fixture revoke", time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				writeCapturePassport(t, f.spawnClient, "alice", "dev", false)
			}
			close(release)
			select {
			case code := <-result:
				if code != http.StatusForbidden {
					t.Fatalf("pre-commit %s admitted: %d", mode, code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("capture did not settle after authorization change")
			}
			if blocks, rows := leafEntries(t, vaultRoot); blocks != 0 || rows != 0 {
				t.Fatalf("pre-commit %s wrote vault: blocks=%d rows=%d", mode, blocks, rows)
			}
		})
	}
}
