// Package replay_test pins leaf replay (SPEC-0085 R11.5): the prov.db
// audit spine re-materializes inside another provider — idempotent on
// retry, resumable after interruption, scope-minted per entry audience.
package replay_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
	"github.com/letya999/hermes-hub/internal/memory/memorytest"
	"github.com/letya999/hermes-hub/internal/memory/providers/hindsight"
	"github.com/letya999/hermes-hub/internal/memory/replay"
)

// entriesFor seeds a prov.db-shaped stream in memory — the same order and
// field mix a real sidecar replays.
func entriesFor(scopeKey string) []memory.Entry {
	scope := memory.Scope{Key: scopeKey}
	day := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	out := []memory.Entry{}
	for i, body := range []string{"owner fact one", "agent note two", "routine fact three"} {
		out = append(out, memory.Entry{
			ID:         "2026-10-20-" + string(rune('1'+i)),
			Scope:      scope,
			Kind:       memory.KindInteractive,
			Provenance: memory.ProvenanceOwner,
			Audience:   memory.AudiencePrivate,
			Body:       body,
			ObservedAt: day,
		})
	}
	return out
}

// Replaying the same stream twice stores the identical set — provider-side
// upsert plus entry ids make every retry a no-op (SPEC-0085 R11.5).
func TestReplayIdempotent(t *testing.T) {
	dir := t.TempDir()
	p := memorytest.NewFake(t)
	entries := entriesFor("src-scope")
	mint := func(a memory.Audience) memory.Scope {
		return memory.MintScope([]byte("mat"), "prod", "alice", string(a))
	}
	r := replay.New(p, mint, filepath.Join(dir, "replay.cursor"))
	rep, err := r.Run(context.Background(), entries)
	if err != nil {
		t.Fatalf("first replay: %v", err)
	}
	if rep.Captured != 3 {
		t.Fatalf("first replay captured %d, want 3", rep.Captured)
	}
	rep2, err := r.Run(context.Background(), entries)
	if err != nil {
		t.Fatalf("second replay: %v", err)
	}
	if rep2.Captured != 0 {
		t.Fatalf("second replay re-captured %d entries", rep2.Captured)
	}
	exported, err := p.Export(context.Background(), mint(memory.AudiencePrivate))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 3 {
		t.Fatalf("provider holds %d entries after two replays, want 3", len(exported))
	}
}

// flaky fails the failAt-th capture call — a mid-run provider outage the
// cursor must survive.
type flaky struct {
	inner  memorytest.Provider
	failAt int
	calls  int
}

func (f *flaky) Caps(ctx context.Context) memory.Caps { return f.inner.Caps(ctx) }
func (f *flaky) Capture(ctx context.Context, e memory.Entry) (string, error) {
	f.calls++
	if f.calls == f.failAt {
		return "", fmt.Errorf("provider down")
	}
	return f.inner.Capture(ctx, e)
}
func (f *flaky) Recall(ctx context.Context, s memory.Scope) (memory.Block, error) {
	return f.inner.Recall(ctx, s)
}
func (f *flaky) Search(ctx context.Context, s memory.Scope, q memory.Query) ([]memory.Hit, error) {
	return f.inner.Search(ctx, s, q)
}
func (f *flaky) Write(ctx context.Context, op memory.WriteOp) (memory.WriteResult, error) {
	return f.inner.Write(ctx, op)
}
func (f *flaky) Export(ctx context.Context, s memory.Scope) ([]memory.Entry, error) {
	return f.inner.Export(ctx, s)
}
func (f *flaky) Health(ctx context.Context) error { return f.inner.Health(ctx) }

// A replay killed mid-stream resumes at the cursor — already-captured
// entries are never re-sent, and the run completes the remaining range
// (SPEC-0085 R11.5 resumability).
func TestReplayResumable(t *testing.T) {
	dir := t.TempDir()
	p := &flaky{inner: memorytest.NewFake(t), failAt: 3}
	entries := entriesFor("src-scope")
	mint := func(a memory.Audience) memory.Scope {
		return memory.MintScope([]byte("mat"), "prod", "alice", string(a))
	}
	r := replay.New(p, mint, filepath.Join(dir, "replay.cursor"))
	rep, err := r.Run(context.Background(), entries)
	if err == nil || rep.Captured != 2 {
		t.Fatalf("interrupted replay: rep=%+v err=%v", rep, err)
	}
	if rep.Done {
		t.Fatal("interrupted run must not mark done")
	}
	// Resume: the same stream completes the tail without re-capturing.
	rep2, err := r.Run(context.Background(), entries)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rep2.Captured != 1 || !rep2.Done {
		t.Fatalf("resume captured %d done=%v, want 1/true", rep2.Captured, rep2.Done)
	}
	exported, err := p.Export(context.Background(), mint(memory.AudiencePrivate))
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(exported) != 3 {
		t.Fatalf("provider holds %d entries after resume, want 3", len(exported))
	}
}

// A clean provider populated by replay returns the same search evidence as
// the source stream produced — parity at the wire contract level
// (SPEC-0085 R11.5; fixture verification, live parity pending CHG-0239).
func TestReplaySearchParity(t *testing.T) {
	dir := t.TempDir()
	mint := func(a memory.Audience) memory.Scope {
		return memory.MintScope([]byte("parity-material"), "prod", "alice", string(a))
	}
	entries := entriesFor("src-scope")

	// Baseline: the same entries captured directly into one fixture bank —
	// the hit set the source content produces.
	_, srvA := memorytest.NewHindsightFixture(t)
	srcP, err := hindsight.New(srvA.URL)
	if err != nil {
		t.Fatalf("source provider: %v", err)
	}
	for _, e := range entries {
		e.Scope = mint(e.Audience)
		if _, err := srcP.Capture(context.Background(), e); err != nil {
			t.Fatalf("source capture: %v", err)
		}
	}
	want, err := srcP.Search(context.Background(), mint(memory.AudiencePrivate), memory.Query{Text: "fact", Limit: 10})
	if err != nil {
		t.Fatalf("source search: %v", err)
	}

	// Replay into a clean provider: identical hit set, id for id.
	fxB, srvB := memorytest.NewHindsightFixture(t)
	_ = fxB
	dstP, err := hindsight.New(srvB.URL)
	if err != nil {
		t.Fatalf("replay provider: %v", err)
	}
	r := replay.New(dstP, mint, filepath.Join(dir, "replay.cursor"))
	if _, err := r.Run(context.Background(), entries); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := dstP.Search(context.Background(), mint(memory.AudiencePrivate), memory.Query{Text: "fact", Limit: 10})
	if err != nil {
		t.Fatalf("replay search: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("parity: source %d hits, replay %d", len(want), len(got))
	}
	// The fixture has no recall ranking — parity is set equality.
	byRef := func(h []memory.Hit) func(i, j int) bool {
		return func(i, j int) bool { return h[i].Ref < h[j].Ref }
	}
	sort.Slice(got, byRef(got))
	sort.Slice(want, byRef(want))
	for i := range want {
		if got[i].Ref != want[i].Ref || got[i].Snippet != want[i].Snippet {
			t.Fatalf("hit %d drifted: %+v vs %+v", i, got[i], want[i])
		}
	}
}

// Cursor failures are honest: a corrupt checkpoint refuses to resume (never
// silently re-captures or skips), and an unwritable cursor path surfaces
// the write error instead of pretending progress was saved.
func TestReplayCursorErrors(t *testing.T) {
	dir := t.TempDir()
	mint := func(a memory.Audience) memory.Scope {
		return memory.MintScope([]byte("m"), "prod", "alice", string(a))
	}
	cursor := filepath.Join(dir, "replay.cursor")
	if err := os.WriteFile(cursor, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := replay.New(memorytest.NewFake(t), mint, cursor)
	if _, err := r.Run(context.Background(), entriesFor("s")); err == nil {
		t.Fatal("corrupt cursor must refuse to run")
	}

	bad := filepath.Join(dir, "no-such-dir", "replay.cursor")
	r2 := replay.New(memorytest.NewFake(t), mint, bad)
	if _, err := r2.Run(context.Background(), entriesFor("s")); err == nil {
		t.Fatal("unwritable cursor path must surface the save error")
	}
}
