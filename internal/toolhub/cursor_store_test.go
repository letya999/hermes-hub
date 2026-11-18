package toolhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readCursorFile(t *testing.T, path string) map[string]uint64 {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cursor file: %v", err)
	}
	out := map[string]uint64{}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("parse cursor file: %v", err)
	}
	return out
}

func TestCursorStoreAdvanceFlushReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors.json")
	cs, err := LoadCursorStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.CompareAndAdvance("k", 0, 42) {
		t.Fatal("first advance refused")
	}
	// First flush always lands: flushed starts at the zero time, so the
	// interval gate is open.
	if err := cs.Flush(time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := readCursorFile(t, path)["k"]; got != 42 {
		t.Fatalf("flushed cursor=%d, want 42", got)
	}
	reloaded, err := LoadCursorStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("k"); got != 42 {
		t.Fatalf("reloaded cursor=%d, want 42", got)
	}
}

func TestCursorStoreCASLosesToConcurrentAdvance(t *testing.T) {
	cs, err := LoadCursorStore("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.CompareAndAdvance("k", 0, 10) || !cs.CompareAndAdvance("k", 10, 20) {
		t.Fatal("sequential advances refused")
	}
	if cs.CompareAndAdvance("k", 10, 30) {
		t.Fatal("stale expected cursor won the race")
	}
	if got := cs.Get("k"); got != 20 {
		t.Fatalf("cursor=%d, want 20", got)
	}
}

func TestCursorStoreFileWinsOverSeed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors.json")
	body, _ := json.Marshal(map[string]uint64{"k": 99})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	cs, err := LoadCursorStore(path, map[string]uint64{"k": 3, "other": 7})
	if err != nil {
		t.Fatal(err)
	}
	// The sidecar is authoritative for keys it holds; seed covers only keys
	// the file never saw — the pre-cutover registry migration.
	if cs.Get("k") != 99 || cs.Get("other") != 7 {
		t.Fatalf("seed merge wrong: k=%d other=%d", cs.Get("k"), cs.Get("other"))
	}
}

func TestCursorStoreFlushHonoursInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors.json")
	cs, err := LoadCursorStore(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !cs.CompareAndAdvance("k", 0, 1) {
		t.Fatal("advance refused")
	}
	if err := cs.Flush(now); err != nil {
		t.Fatal(err)
	}
	if !cs.CompareAndAdvance("k", 1, 2) {
		t.Fatal("second advance refused")
	}
	// Inside the interval the file keeps the last durable value — this is the
	// batching that makes the hot path cheap; the crash window is the price.
	if err := cs.Flush(now.Add(cursorFlushInterval / 2)); err != nil {
		t.Fatal(err)
	}
	if got := readCursorFile(t, path)["k"]; got != 1 {
		t.Fatalf("early flush wrote %d, want batched value 1", got)
	}
	if err := cs.Flush(now.Add(cursorFlushInterval)); err != nil {
		t.Fatal(err)
	}
	if got := readCursorFile(t, path)["k"]; got != 2 {
		t.Fatalf("due flush wrote %d, want 2", got)
	}
}

func TestCursorStoreSeedsFromRegistryWhenFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors.json")
	cs, err := LoadCursorStore(path, map[string]uint64{"migrated": 17})
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.Get("migrated"); got != 17 {
		t.Fatalf("seeded cursor=%d, want 17", got)
	}
}
