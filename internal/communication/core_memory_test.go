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

	"github.com/letya999/hermes-hub/internal/vault"
)

// coreFixture builds a vault with CORE content, a fake runtime that captures
// the per-run instructions field, and an enqueued owner-DM job.
func coreFixture(t *testing.T, g *Gateway) (Job, string, string, *string) {
	t.Helper()
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(vaultRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n- water the ferns\n\n## Facts\n- owner prefers tea\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var gotInstructions string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		// The terminal run also triggers a /v1/usage call whose body carries
		// no instructions field — only run-submission paths may overwrite.
		if r.URL.Path == "/v1/execute" || r.URL.Path == "/v1/resume" {
			gotInstructions, _ = body["instructions"].(string)
		}
		jobID, _ := body["job_id"].(string)
		_ = json.NewEncoder(w).Encode(map[string]any{"job_id": jobID, "run_id": "r1", "session_id": "s1", "status": "completed", "text": "ok"})
	}))
	t.Cleanup(server.Close)
	user := g.user("alice")
	job := Job{Envelope: user.Envelope(11), ID: "core-job-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "core-job-1", Text: "hi"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	return job, vaultRoot, server.URL, &gotInstructions
}

// SPEC-0085 R1.6: private-audience runs receive the owner CORE block in
// per-run instructions — the curated directives/facts reach the prompt.
func TestCorePrivateOnly(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Directives", "water the ferns", "## Facts", "prefers tea"} {
		if !strings.Contains(*got, want) {
			t.Fatalf("private run instructions missing CORE %q: %q", want, *got)
		}
	}
}

// SPEC-0085 R1.6: a group-audience run gets zero private CORE bytes — the
// audience check fails closed and the block never leaves the vault.
func TestGroupPromptExcludesCore(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	job.ID, job.IdempotencyKey, job.ScopeID = "core-group-1", "core-group-1", "group:alice"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"## Directives", "water the ferns", "## Facts", "prefers tea", "Owner-curated memory"} {
		if strings.Contains(*got, leak) {
			t.Fatalf("group prompt leaked private CORE %q: %q", leak, *got)
		}
	}
}

// SPEC-0085 R1.6: the admitted run's CORE snapshot is pinned on the durable
// mapping — a retried run must reuse it byte-for-byte while a later CORE
// edit only reaches the next job (same discipline as the style snapshot).
func TestCorePinnedAcrossRetries(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, _, _ := coreFixture(t, g)
	first, err := g.spool.CoreForRun(job, vaultRoot)
	if err != nil || !strings.Contains(first, "water the ferns") {
		t.Fatalf("first snapshot: %q %v", first, err)
	}
	// The owner edits CORE between admission and retry — the admitted run
	// still carries its pinned snapshot, not the live file.
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n- replaced directives\n\n## Facts\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := g.spool.CoreForRun(job, vaultRoot)
	if err != nil || again != first {
		t.Fatalf("retry reinterpreted the snapshot: %q vs %q", again, first)
	}
	// A new job pins the current file instead.
	job2 := job
	job2.ID, job2.IdempotencyKey = "core-job-2", "core-job-2"
	if _, err := g.spool.Enqueue(job2); err != nil {
		t.Fatal(err)
	}
	fresh, err := g.spool.CoreForRun(job2, vaultRoot)
	if err != nil || !strings.Contains(fresh, "replaced directives") {
		t.Fatalf("new job kept the stale snapshot: %q", fresh)
	}
	// A group-scope job never pins private content.
	groupJob := job
	groupJob.ID, groupJob.IdempotencyKey, groupJob.ScopeID = "core-group-2", "core-group-2", "group:alice"
	if _, err := g.spool.Enqueue(groupJob); err != nil {
		t.Fatal(err)
	}
	if block, err := g.spool.CoreForRun(groupJob, vaultRoot); err != nil || block != "" {
		t.Fatalf("group job received CORE: %q %v", block, err)
	}
}

// The pin protects the wire too: a retried run re-sends its admitted
// snapshot even after the owner has edited CORE.md.
func TestCorePinnedRunRetry(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*got, "water the ferns") {
		t.Fatalf("first run missing CORE: %q", *got)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n- replaced directives\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RunOutcome(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*got, "water the ferns") || strings.Contains(*got, "replaced directives") {
		t.Fatalf("retry did not reuse the pinned snapshot: %q", *got)
	}
}

// The runtime admission ceiling is 4096 instruction bytes; the assembled
// field — style, voice, language hint plus the CORE block — must stay within
// it or the run is refused. CORE yields first: it is truncated to fit.
func TestCoreBoundedWithinInstructions(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(vaultRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "CORE.md"), []byte("# CORE\n\n## Directives\n"+strings.Repeat("- d\n", 1500)+"\n## Facts\n"+strings.Repeat("- f\n", 1500)), 0600); err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "styled-core", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.SetTaskStyle(user, 11, task.TaskID, strings.Repeat("s", 1000)); err != nil {
		t.Fatal(err)
	}
	envelope := user.Envelope(11)
	envelope.ConversationID = task.ConversationID
	job := Job{Envelope: envelope, ID: "core-bounded", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "core-bounded", ChatID: 11, TaskID: task.TaskID, Text: "hi"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
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
	runner := HTTPRunner{URL: server.URL, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot, Voice: true}
	if _, err := runner.Run(context.Background(), job, user); err != nil {
		t.Fatal(err)
	}
	if len(gotInstructions) > 4096 {
		t.Fatalf("instructions exceed the admission ceiling: %d bytes", len(gotInstructions))
	}
}
