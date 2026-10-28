package skillstore

import (
	"testing"
)

// R12.5/R11.1: usage events land in the store-private .state dir with
// per-skill counters — never in the agent-visible tree.
func TestRecordUsageCountsAndStaysPrivate(t *testing.T) {
	root := t.TempDir()
	if err := RecordUsage(root, "pdf", "use", ""); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := RecordUsage(root, "pdf", "helped", "fixed the merge order"); err != nil {
		t.Fatalf("record: %v", err)
	}
	u, err := Usage(root, "pdf")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if u.Stats.Uses != 1 || u.Stats.Helped != 1 || u.Stats.LastUsed == "" {
		t.Fatalf("stats %+v", u.Stats)
	}
	if len(u.Events) != 2 || u.Events[1].Kind != "helped" || u.Events[1].Detail != "fixed the merge order" {
		t.Fatalf("events %+v", u.Events)
	}
	s := &Store{root: root}
	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == ".state" || n == "usage" {
			t.Fatalf("usage state leaked into skill names: %v", names)
		}
	}
}
