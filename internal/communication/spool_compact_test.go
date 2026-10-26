package communication

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// Terminal history older than retention is not simply deleted: it lands in a
// compressed daily bundle so a multi-week incident still has an audit trail
// while the hot dirs stay small.
func TestCompactArchivesTerminalHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	write := func(dir, id string, v any) string {
		t.Helper()
		path := filepath.Join(root, dir, id+".json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := atomicJSON(path, v); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("done", "old-job", Job{ID: "old-job"})
	write(filepath.Join("outbox", "done"), "old-out", Delivery{ID: "old-out"})
	write(filepath.Join("outbox", "failed"), "old-fail", Delivery{ID: "old-fail"})
	s.mu.Lock()
	for _, m := range []JobMapping{
		{JobID: "fj-terminal", IdempotencyKey: "k-t", Status: "completed"},
		{JobID: "fj-uncertain", IdempotencyKey: "k-u", Status: "uncertain", NextObservationAt: time.Now().Add(time.Hour)},
	} {
		if err := s.writeMappingLocked(m); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	s.mu.Unlock()
	write("failed", "fj-terminal", Job{ID: "fj-terminal"})
	write("failed", "fj-uncertain", Job{ID: "fj-uncertain"})
	fresh := write("done", "fresh-job", Job{ID: "fresh-job"})
	_ = os.Chtimes(fresh, time.Now(), time.Now())

	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"done", filepath.Join("outbox", "done"), filepath.Join("outbox", "failed")} {
		entries, _ := os.ReadDir(filepath.Join(root, dir))
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "old") {
				t.Fatalf("aged %s record survived compaction", dir)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "failed", "fj-terminal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("terminal failed job survived compaction")
	}
	if _, err := os.Stat(filepath.Join(root, "failed", "fj-uncertain.json")); err != nil {
		t.Fatal("uncertain failed job compacted while still observable")
	}
	if _, err := os.Stat(filepath.Join(root, "done", "fresh-job.json")); err != nil {
		t.Fatal("fresh done record compacted")
	}
	archives, err := filepath.Glob(filepath.Join(root, "archive", "*.jsonl.gz"))
	if err != nil || len(archives) == 0 {
		t.Fatalf("no compressed archive written: %v", err)
	}
	var seen string
	for _, path := range archives {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("archive %s not gzip: %v", path, err)
		}
		body, _ := io.ReadAll(gz)
		_ = gz.Close()
		_ = f.Close()
		seen += string(body)
	}
	for _, id := range []string{"old-job", "old-out", "old-fail", "fj-terminal"} {
		if !strings.Contains(seen, id) {
			t.Fatalf("archived record %s missing from %s", id, archives)
		}
	}
}

// A burst of terminal records inside the retention window still caps the hot
// dir: overflow is archived oldest-first, never dropped silently.
func TestCompactCapsTerminalFileCount(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := range 6 {
		path := filepath.Join(root, "done", "job-"+string(rune('a'+i))+".json")
		if err := atomicJSON(path, Job{ID: "job-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(now.Add(time.Minute), 336*time.Hour, CompactLimits{MaxTerminalFiles: 4, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "done"))
	if len(entries) != 4 {
		t.Fatalf("terminal dir over cap: %d", len(entries))
	}
	for _, e := range entries {
		if e.Name() == "job-a.json" || e.Name() == "job-b.json" {
			t.Fatalf("oldest record %s kept over cap", e.Name())
		}
	}
	archives, err := filepath.Glob(filepath.Join(root, "archive", "*.jsonl.gz"))
	if err != nil || len(archives) == 0 {
		t.Fatal("overflow not archived")
	}
}

// Routing and delivery history compresses too: an aged terminal mapping and
// an aged non-live event receipt land in the archive, not the void.
func TestCompactArchivesMappingsAndReceipts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	s.mu.Lock()
	if err := s.writeMappingLocked(JobMapping{JobID: "m-old", IdempotencyKey: "k-m", Status: "completed"}); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	if err := os.Chtimes(s.mappingPath("m-old"), old, old); err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(root, "events", "receipt-1.json")
	if err := atomicJSON(event, map[string]string{"delivery_id": "receipt-1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(event, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	var seen string
	archives, _ := filepath.Glob(filepath.Join(root, "archive", "*.jsonl.gz"))
	for _, path := range archives {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(gz)
		_ = gz.Close()
		_ = f.Close()
		seen += string(body)
	}
	for _, id := range []string{"m-old", "receipt-1"} {
		if !strings.Contains(seen, id) {
			t.Fatalf("%s not archived: %s", id, archives)
		}
	}
	if _, err := os.Stat(s.idempotencyIndexPath("k-m")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("idempotency index outlived its mapping")
	}
}

// Event receipts honour the same count cap: a flood of settled receipts
// overflows into the archive while one still delivering stays hot.
func TestCompactCapsEventReceipts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := range 5 {
		id := "rcpt-" + string(rune('a'+i))
		path := filepath.Join(root, "events", id+".json")
		if err := atomicJSON(path, map[string]string{"delivery_id": id}); err != nil {
			t.Fatal(err)
		}
		at := now.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	live := filepath.Join(root, "outbox", "pending", "d-live.json")
	if err := os.MkdirAll(filepath.Dir(live), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(live, Delivery{ID: "d-live"}); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(root, "events", "rcpt-live.json"), streamReceipt{Delivery: &Delivery{ID: "d-live"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(now.Add(time.Minute), 336*time.Hour, CompactLimits{MaxTerminalFiles: 4, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "events"))
	if len(entries) != 5 {
		t.Fatalf("events over cap or live receipt evicted: %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(root, "events", "rcpt-live.json")); err != nil {
		t.Fatal("live receipt evicted")
	}
}

// Live records pinned by a guard never leave the hot dir and do not count
// toward the terminal-file cap either — an uncertain flood must not evict
// every archivable record just to satisfy the bound.
func TestCompactCapIgnoresGuardedLiveRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	for i := range 5 {
		m := JobMapping{JobID: "live-" + string(rune('a'+i)), Status: "uncertain", NextObservationAt: time.Now().Add(time.Hour)}
		if err := s.writeMappingLocked(m); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	for i := range 3 {
		m := JobMapping{JobID: "term-" + string(rune('a'+i)), Status: "completed"}
		if err := s.writeMappingLocked(m); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	s.mu.Unlock()
	for i := range 5 {
		if err := atomicJSON(filepath.Join(root, "failed", "live-"+string(rune('a'+i))+".json"), Job{ID: "live-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		if err := atomicJSON(filepath.Join(root, "failed", "term-"+string(rune('a'+i))+".json"), Job{ID: "term-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 4, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, "failed"))
	if len(entries) != 8 {
		t.Fatalf("cap evicted records under the terminal count: %d", len(entries))
	}
}

// The metering journal stays bounded: records older than retention move to
// the archive, except the newest entry per session — evicting that baseline
// would corrupt delta accounting for a still-open session.
func TestCompactBoundsUsageJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := now.Add(-100 * time.Hour)
	ptr := int64ptr
	entries := []usageEntry{
		{Version: 2, ScopeID: "user:a", ContextID: "c", SessionID: "s1", JobID: "j0", RunID: "r0", MeasuredAt: old, InputCounter: ptr(100), OutputCounter: ptr(40), InputTokens: ptr(100), OutputTokens: ptr(40), InitialSnapshot: true},
		{Version: 2, ScopeID: "user:a", ContextID: "c", SessionID: "s1", JobID: "j1", RunID: "r1", MeasuredAt: old.Add(time.Hour), InputCounter: ptr(200), OutputCounter: ptr(80), InputTokens: ptr(100), OutputTokens: ptr(40)},
		{Version: 2, ScopeID: "user:a", ContextID: "c", SessionID: "s2", JobID: "j2", RunID: "r2", MeasuredAt: old.Add(2 * time.Hour), InputCounter: ptr(50), OutputCounter: ptr(10), InputTokens: ptr(50), OutputTokens: ptr(10), InitialSnapshot: true},
		{Version: 2, ScopeID: "user:a", ContextID: "c", SessionID: "s1", JobID: "j3", RunID: "r3", MeasuredAt: now.Add(-time.Hour), InputCounter: ptr(250), OutputCounter: ptr(90), InputTokens: ptr(50), OutputTokens: ptr(10)},
	}
	f, err := os.Create(usagePath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, _ := json.Marshal(e)
		if _, err := f.Write(append(body, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(now, 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	kept, err := s.usageEntriesLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 {
		t.Fatalf("journal not bounded: %d entries", len(kept))
	}
	if kept[0].JobID != "j2" || kept[1].JobID != "j3" {
		t.Fatalf("wrong survivors: %s %s", kept[0].JobID, kept[1].JobID)
	}
	job := testJob("j4", "user:a")
	job.ContextID = "c"
	report := hubruntime.SessionUsage{SessionFound: true, SessionID: "s2", InputTokens: ptr(90), OutputTokens: ptr(30)}
	if err := s.RecordUsage(job, "r4", report); err != nil {
		t.Fatal(err)
	}
	final, err := s.usageEntriesLocked()
	if err != nil {
		t.Fatal(err)
	}
	last := final[len(final)-1]
	if last.InitialSnapshot || last.InputTokens == nil || *last.InputTokens != 40 {
		t.Fatalf("baseline lost: %+v", last)
	}
	var archived string
	bundles, _ := filepath.Glob(filepath.Join(root, "archive", "usage-*.jsonl.gz"))
	for _, path := range bundles {
		fh, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(fh)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(gz)
		_ = gz.Close()
		_ = fh.Close()
		archived += string(body)
	}
	if !strings.Contains(archived, `"j0"`) || !strings.Contains(archived, `"j1"`) {
		t.Fatalf("evicted usage records missing from archive: %q", archived)
	}
}

// An entry cap bounds the journal inside the retention window too: oldest
// non-baseline records leave first; only when nothing else remains do the
// oldest baselines drop. Evictions still land in the archive.
func TestCompactCapsUsageJournalEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	write := func(entries []usageEntry) {
		t.Helper()
		f, err := os.Create(usagePath(root))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			body, _ := json.Marshal(e)
			if _, err := f.Write(append(body, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	entry := func(session, job string, at time.Time) usageEntry {
		return usageEntry{Version: 2, ScopeID: "user:a", ContextID: "c", SessionID: session, JobID: job, RunID: "r-" + job, MeasuredAt: at}
	}
	write([]usageEntry{
		entry("s1", "a", now.Add(-5*time.Hour)),
		entry("s1", "b", now.Add(-4*time.Hour)),
		entry("s1", "c", now.Add(-3*time.Hour)),
		entry("s2", "d", now.Add(-2*time.Hour)),
		entry("s2", "e", now.Add(-time.Hour)),
		entry("s2", "f", now),
	})
	if err := s.Compact(now, 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20, MaxUsageEntries: 4}); err != nil {
		t.Fatal(err)
	}
	kept, err := s.usageEntriesLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 4 {
		t.Fatalf("journal over cap: %d", len(kept))
	}
	var ids []string
	for _, e := range kept {
		ids = append(ids, e.JobID)
	}
	for _, want := range []string{"c", "f"} {
		found := false
		for _, id := range ids {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("baseline %s evicted before older records: %v", want, ids)
		}
	}
	bundles, _ := filepath.Glob(filepath.Join(root, "archive", "usage-*.jsonl.gz"))
	if len(bundles) == 0 {
		t.Fatal("cap evictions not archived")
	}
}

// Archives honour the same retention bound; the byte cap evicts oldest first.
func TestCompactPurgesAgedArchives(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-100 * time.Hour)
	path := filepath.Join(root, "archive", "done-20260101.jsonl.gz")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 72*time.Hour, CompactLimits{MaxTerminalFiles: 2048, ArchiveMaxBytes: 64 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aged archive survived retention")
	}
}
