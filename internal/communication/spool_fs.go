package communication

import (
	"path/filepath"
	"strings"
)

// Record classes whose JSON ledgers move into spool.db (SPEC-0090 R3.1).
var spoolRecordDirs = map[string]bool{
	"pending": true, "running": true, "done": true, "failed": true,
	"outbox": true, "mappings": true, "conversations": true,
	"event-state": true, "run-events": true, "provider-dedup": true,
	"occurrences": true, "schedules": true, "tasks": true,
	"callbacks": true, "inbound": true, "watchers": true,
	"digests": true, "goals": true, "assignments": true,
	"approval-messages": true,
}

// Top-level JSON journals that are records like any class member.
var spoolRecordFiles = map[string]bool{
	"budget.json": true, "usage.jsonl": true, "canary.jsonl": true,
	"event-layout.json": true, "offset.json": true,
	"admission-cursor.json": true,
}

func spoolRecordSuffix(name string) bool {
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".initialized")
}

// spoolRecordPath reports whether a path under the spool root is a durable
// record absorbed by spool.db. Binary payloads, scratch, mock ledgers and
// migration artifacts stay plain files (SPEC-0090 R3.2); the `events`
// sentinel that fences the retired legacy layout stays a file as well.
func spoolRecordPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	rel = filepath.ToSlash(rel)
	top := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		top = rel[:i]
	}
	if top == "" {
		return false
	}
	if !strings.Contains(rel, "/") {
		return spoolRecordFiles[top]
	}
	if !spoolRecordDirs[top] {
		return false
	}
	name := filepath.Base(rel)
	if strings.Contains(name, ".substrate-backup") || strings.HasSuffix(name, ".legacy") || strings.HasSuffix(name, ".manifest.json") {
		return false
	}
	return spoolRecordSuffix(name)
}
