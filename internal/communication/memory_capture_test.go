package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

func TestSiblingRunGetsNoCoreOrRecall(t *testing.T) {
	c := baseTestConfig(t)
	bobState, bobWS := filepath.Join(t.TempDir(), "bob-state"), filepath.Join(t.TempDir(), "bob-ws")
	_ = os.MkdirAll(bobState, 0700)
	_ = os.MkdirAll(bobWS, 0700)
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: bobState, WorkspaceDir: bobWS})
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(vaultRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n- water the ferns\n\n## Facts\n- owner prefers tea\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = vaultRoot
	var gotInstructions string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.URL.Path == "/v1/execute" {
			_ = json.NewDecoder(r.Body).Decode(&body)
			gotInstructions, _ = body["instructions"].(string)
		}
		jobID, _ := body["job_id"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": jobID, "run_id": "r1", "session_id": "s1", "status": "completed", "text": "ok"})
	}))
	defer server.Close()
	c.RuntimeURL = server.URL
	c.RuntimeAuth = "auth"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	bob := g.user("bob")
	bobJob := Job{Envelope: bob.Envelope(22), ID: "sib-core-1", OrganizationID: "personal", UserID: "bob", ActorID: "bob", ScopeID: "user:bob", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "sib-core-1", Text: "hi"}
	if _, err := g.spool.Enqueue(bobJob); err != nil {
		t.Fatal(err)
	}
	if _, err := g.runner.Run(context.Background(), bobJob, bob); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"water the ferns", "prefers tea", "Owner-curated memory"} {
		if strings.Contains(gotInstructions, leak) {
			t.Fatalf("sibling run leaked owner memory %q: %q", leak, gotInstructions)
		}
	}
	aliceJob := Job{Envelope: g.user("alice").Envelope(11), ID: "own-core-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "own-core-1", Text: "hi"}
	if _, err := g.spool.Enqueue(aliceJob); err != nil {
		t.Fatal(err)
	}
	if _, err := g.runner.Run(context.Background(), aliceJob, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotInstructions, "water the ferns") {
		t.Fatalf("owner run lost its CORE block: %q", gotInstructions)
	}
}

type runnerFunc func(context.Context, Job, User) (string, error)

func (f runnerFunc) Run(ctx context.Context, job Job, u User) (string, error) {
	return f(ctx, job, u)
}

// The sibling gate holds through dispatch too: a sibling job in flight
// must not mint a run-scope stamp in the owner vault, while the owner's
// own job stamps private exactly as before.
func TestRunScopeSiblingNeverStamps(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	bobState, bobWS := filepath.Join(t.TempDir(), "bob-state"), filepath.Join(t.TempDir(), "bob-ws")
	_ = os.MkdirAll(bobState, 0700)
	_ = os.MkdirAll(bobWS, 0700)
	c.Users = append(c.Users, User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: bobState, WorkspaceDir: bobWS})
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan Job, 4)
	release := make(chan struct{}, 4)
	g.api = &fakeAPI{}
	g.runner = runnerFunc(func(_ context.Context, job Job, _ User) (string, error) {
		entered <- job
		<-release
		return "answer", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	defer func() { cancel(); <-done }()
	defer close(release)
	enqueue := func(userID string, chatID int64, jobID string) {
		job := Job{Envelope: g.user(userID).Envelope(chatID), IdempotencyKey: jobID, ID: jobID, OrganizationID: "personal", UserID: userID, ActorID: userID, ScopeID: "user:" + userID, Channel: "telegram_bot", Trigger: "message", ChatID: chatID, Text: "hi"}
		if _, err := g.spool.Enqueue(job); err != nil {
			t.Fatal(err)
		}
	}
	enqueue("bob", 22, "job-sib-w")
	running := <-entered
	if running.ID != "job-sib-w" {
		t.Fatalf("unexpected job dispatched: %s", running.ID)
	}
	m := readRunScope(root)
	if _, ok := m.Runs["job-sib-w"]; ok {
		t.Fatalf("sibling job stamped the owner vault: %+v", m.Runs["job-sib-w"])
	}
	release <- struct{}{}
	enqueue("alice", 11, "job-alice-w")
	running = <-entered
	if running.ID != "job-alice-w" {
		t.Fatalf("unexpected job dispatched: %s", running.ID)
	}
	m = readRunScope(root)
	if m.Runs["job-alice-w"].Audience != "private" || m.Runs["job-alice-w"].Provenance != "owner" {
		t.Fatalf("owner job not stamped: %+v", m.Runs)
	}
	release <- struct{}{}
}

// R6.2: the dispatch stamp also mints the turn's provenance — memory_card's
// owner-binding reads it; a forwarded turn never mints "owner".
func TestRunScopeStampsProvenance(t *testing.T) {
	vaultDir := t.TempDir()
	ownerJob := Job{ID: "j-own", Trigger: "message", Untrusted: false}
	fwdJob := Job{ID: "j-fwd", Trigger: "message", Untrusted: true}
	stampRunScope(vaultDir, ownerJob.ID, ownerJob.MemoryAudience(), ownerJob.MemoryTurnProvenance())
	stampRunScope(vaultDir, fwdJob.ID, fwdJob.MemoryAudience(), fwdJob.MemoryTurnProvenance())
	m := readRunScope(vaultDir)
	if m.Runs["j-own"].Provenance != "owner" {
		t.Fatalf("owner job stamped %+v", m.Runs["j-own"])
	}
	if m.Runs["j-fwd"].Provenance != "untrusted" {
		t.Fatalf("forwarded job stamped %+v", m.Runs["j-fwd"])
	}
}

// R3.4 transport: the dispatch stamps the job's audience into the vault's
// run-scope sidecar for the run's duration — private iff it is the only
// live stamp; any group run confines every in-flight call to "group".
func TestRunScopeStamped(t *testing.T) {
	vaultDir := t.TempDir()
	stampRunScope(vaultDir, "j-priv", memory.AudiencePrivate, memory.ProvenanceOwner)
	stampRunScope(vaultDir, "j-grp", memory.AudienceGroup, memory.ProvenanceOwner)
	data, err := os.ReadFile(filepath.Join(vaultDir, "runtime", "run-scope", "current"))
	if err != nil {
		t.Fatalf("stamp file: %v", err)
	}
	var m runScopeMap
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Runs["j-priv"].Audience != "private" || m.Runs["j-grp"].Audience != "group" {
		t.Fatalf("stamps %s", data)
	}
	clearRunScope(vaultDir, "j-grp")
	m = readRunScope(vaultDir)
	if _, ok := m.Runs["j-grp"]; ok || m.Runs["j-priv"].Audience != "private" {
		t.Fatalf("stale group stamp survived clear: %+v", m.Runs)
	}
}

// The worker stamps before dispatch and clears after — the file is live only
// inside the run.
func TestRunScopeWorkerLifecycle(t *testing.T) {
	c := baseTestConfig(t)
	root := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(root); err != nil {
		t.Fatal(err)
	}
	c.VaultDir = root
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.config.VaultDir = root
	job := Job{ID: "j-scope", Text: "hi", Channel: "telegram_bot", ScopeID: "group:me", UserID: "alice"}
	g.config.VaultDir = root
	stampRunScope(g.config.VaultDir, job.ID, job.MemoryAudience(), job.MemoryTurnProvenance())
	m := readRunScope(root)
	if m.Runs["j-scope"].Audience != "group" {
		t.Fatalf("group job stamped %+v", m.Runs["j-scope"])
	}
	clearRunScope(g.config.VaultDir, job.ID)
	if m := readRunScope(root); len(m.Runs) != 0 {
		t.Fatalf("stamp not cleared: %+v", m.Runs)
	}
}
