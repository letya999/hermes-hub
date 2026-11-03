package communication

import (
	"path/filepath"
	"testing"
)

// The record router decides which durable paths live in spool.db and which
// stay plain files. Records are JSON ledgers under known classes or the
// top-level journals; binary payloads, scratch and migration artifacts are
// never absorbed.
func TestRecordRoute(t *testing.T) {
	root := filepath.Join("spool")
	cases := []struct {
		path   string
		record bool
	}{
		{filepath.Join(root, "pending", "job-1.json"), true},
		{filepath.Join(root, "running", "job-1.json"), true},
		{filepath.Join(root, "outbox", "pending", "d-1.json"), true},
		{filepath.Join(root, "mappings", "by-key", "k-1.json"), true},
		{filepath.Join(root, "conversations", "c-1.json"), true},
		{filepath.Join(root, "event-state", "s-1.json"), true},
		{filepath.Join(root, "event-state", "s-1.json.initialized"), true},
		{filepath.Join(root, "run-events", "e-1.json"), true},
		{filepath.Join(root, "provider-dedup", "p-1.json"), true},
		{filepath.Join(root, "occurrences", "o-1.json"), true},
		{filepath.Join(root, "schedules", "s-1.json"), true},
		{filepath.Join(root, "tasks", "current", "t-1.json"), true},
		{filepath.Join(root, "tasks", "topic", "t-1.json"), true},
		{filepath.Join(root, "callbacks", "cb-1.json"), true},
		{filepath.Join(root, "inbound", "m-1.json"), true},
		{filepath.Join(root, "watchers", "w-1.json"), true},
		{filepath.Join(root, "digests", "d-1.json"), true},
		{filepath.Join(root, "goals", "g-1.json"), true},
		{filepath.Join(root, "assignments", "a-1.json"), true},
		{filepath.Join(root, "approval-messages", "a-1.json"), true},
		{filepath.Join(root, "budget.json"), true},
		{filepath.Join(root, "usage.jsonl"), true},
		{filepath.Join(root, "canary.jsonl"), true},
		{filepath.Join(root, "event-layout.json"), true},

		{filepath.Join(root, "inbound", "m-1.bin"), false},
		{filepath.Join(root, "tmp", "scratch-1"), false},
		{filepath.Join(root, "archive", "job-1.gz"), false},
		{filepath.Join(root, "blobs", "b-1"), false},
		{filepath.Join(root, "mock-sent", "d-1.json"), false},
		{filepath.Join(root, "mock-links", "123.json"), false},
		{filepath.Join(root, "credentials", "x"), false},
		{filepath.Join(root, "quarantine", "q-1.json"), false},
		{filepath.Join(root, "events"), false},
		{filepath.Join(root, "spool.db"), false},
		{filepath.Join(root, "spool.db.manifest.json"), false},
		{filepath.Join(root, ".writer.lock"), false},
		{filepath.Join(root, "records.activated"), false},
		{filepath.Join(root, "records-legacy", "pending", "job-1.json"), false},
		{filepath.Join(root, "pending", "job-1.json.substrate-backup"), false},
		{filepath.Join(root, "pending", "job-1.json.legacy"), false},
		{filepath.Join(root, "outside", "x.json"), false},
	}
	for _, c := range cases {
		if got := spoolRecordPath(root, c.path); got != c.record {
			t.Errorf("spoolRecordPath(%q) = %v, want %v", c.path, got, c.record)
		}
	}
}
