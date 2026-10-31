package communication

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/vault"
)

// recallFixture builds a vault with one card and an enqueued owner-DM job —
// same shape as coreFixture without the fake runtime.
func recallFixture(t *testing.T, g *Gateway) (Job, string) {
	t.Helper()
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := vault.Ensure(vaultRoot); err != nil {
		t.Fatal(err)
	}
	card := "---\ntype: contact\nstatus: active\nconfidence: high\nsource: owner\ntags: [tea]\naliases: []\ncreated: 2026-04-19\nupdated: 2026-04-19\n---\nowner prefers earl grey\n"
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "cards", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	job := Job{Envelope: user.Envelope(11), ID: "recall-job-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "recall-job-1", Text: "which tea did I pick"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	return job, vaultRoot
}

// SPEC-0085 R4.2: the recall block pins on the durable mapping at admit — a
// retry sends byte-identical bytes even after vault content moves on.
func TestRecallSnapshotPinnedAcrossRetries(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot := recallFixture(t, g)
	first, err := g.spool.RecallForRun(job, vaultRoot)
	if err != nil || !strings.Contains(first, "earl grey") {
		t.Fatalf("first recall: %q %v", first, err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "cards", "tea.md"), []byte("owner switched to coffee\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := g.spool.RecallForRun(job, vaultRoot)
	if err != nil || again != first {
		t.Fatalf("retry reinterpreted the pinned block: %q vs %q", again, first)
	}
}

// SPEC-0085 R4.3: the pinned recall block rides the per-run instructions —
// a private run's prompt carries the framed card hits before any agent
// action.
func TestRecallInInstructions(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	card := "owner prefers earl grey tea blend\n"
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "cards", "tea.md"), []byte(card), 0600); err != nil {
		t.Fatal(err)
	}
	job.Text = "which tea did I pick"
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*got, memory.RecalledOpen) || !strings.Contains(*got, "earl grey") {
		t.Fatalf("instructions missing framed recall block: %q", *got)
	}
}

// SPEC-0085 R4.3/A4.6: an unreadable vault degrades recall to a flagged
// state — the run still completes and the prompt carries the flag rather
// than silently pretending memory access.
func TestRecallDegradedStillRuns(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	if err := os.RemoveAll(vaultRoot); err != nil {
		t.Fatal(err)
	}
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatalf("degraded recall must not eat the run: %v", err)
	}
	if !strings.Contains(*got, "degraded") {
		t.Fatalf("degraded recall must be flagged, not hidden: %q", *got)
	}
}

// SPEC-0085 R4.5/A4.3: a group-scope run's recall slice contains no
// private bytes — the card stays out even though it matches the turn.
func TestRecallAudienceFilter(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot := recallFixture(t, g)
	job.ID, job.IdempotencyKey, job.ScopeID = "recall-group-1", "recall-group-1", "group:alice"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	block, err := g.spool.RecallForRun(job, vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(block, "earl grey") {
		t.Fatalf("group-scope recall leaked a private card: %q", block)
	}
}

// A crafted scope id that merely looks private-adjacent fails closed to
// group — the run's prompt carries neither CORE nor recalled bytes.
func TestGroupRunExcludesPrivateRecall(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot, url, got := coreFixture(t, g)
	if err := os.WriteFile(filepath.Join(vaultRoot, "memory", "cards", "tea.md"), []byte("owner prefers earl grey tea\n"), 0600); err != nil {
		t.Fatal(err)
	}
	job.ID, job.IdempotencyKey, job.ScopeID = "recall-group-2", "recall-group-2", "userx:alice"
	job.Text = "which tea did I pick"
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	runner := HTTPRunner{URL: url, Auth: "auth", Spool: g.spool, VaultDir: vaultRoot}
	if _, err := runner.Run(context.Background(), job, g.user("alice")); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(*got, "earl grey") || strings.Contains(*got, "ferns") {
		t.Fatalf("crafted scope lifted private context into a group run: %q", *got)
	}
}

// SPEC-0085 R4.4/A4.4: whatever the lane injects is marker-wrapped — the
// capture path strips it back out, so recall can never re-enter a leaf.
func TestRecalledBlockMarkedNonExtractable(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	job, vaultRoot := recallFixture(t, g)
	block, err := g.spool.RecallForRun(job, vaultRoot)
	if err != nil || !strings.Contains(block, "earl grey") {
		t.Fatalf("recall: %q %v", block, err)
	}
	stripped := memory.StripRecalled("agent echoes: " + block + " tail")
	if strings.Contains(stripped, "earl grey") {
		t.Fatalf("recalled span survived capture stripping: %q", stripped)
	}
	if !strings.Contains(stripped, "agent echoes: ") || !strings.Contains(stripped, " tail") {
		t.Fatalf("stripping ate unmarked text: %q", stripped)
	}
}
