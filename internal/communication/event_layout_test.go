package communication

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// runEventsRecord builds one valid legacy run-receipt: the migration
// classifier pins filename and schema, so the record has to match production.
func runEventsRecord(jobID, eventID string) (string, []byte) {
	sum := sha256.Sum256([]byte(jobID + "\x00" + eventID))
	name := hex.EncodeToString(sum[:]) + ".json"
	body := []byte(fmt.Sprintf(`{"job_id":%q,"position":1,"event":{"job_id":%q,"event_id":%q,"status":"completed","last_event":"run.completed","text":"done"}}`, jobID, jobID, eventID))
	return name, body
}

// Legacy schedule cursors belonged to the retired automation owner: they must
// be fenced at open, never migrated into a store communication no longer owns.
// Communication-owned records in the same legacy directory still migrate.
func TestEventLayoutFencesLegacyCursorRecord(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	cursor := []byte(`{"schedule_id":"paused-source","cursor":"resume-42","seen":["one"]}`)
	cursorName := spoolFileID("paused-source") + ".json"
	if err := os.WriteFile(filepath.Join(root, "events", cursorName), cursor, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(root); err == nil {
		t.Fatal("legacy schedule cursor silently migrated to the old owner")
	}
	if got, err := os.ReadFile(filepath.Join(root, "events", cursorName)); err != nil || !bytes.Equal(got, cursor) {
		t.Fatal("refused cursor record was modified")
	}
}

func TestEventLayoutMigratesReceiptAndFencesOldWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	name, original := runEventsRecord("job-9", "evt-42")
	if err := os.WriteFile(filepath.Join(root, "events", name), original, 0600); err != nil {
		t.Fatal(err)
	}
	// A run receipt always accompanies its admission mapping in production —
	// recovery cross-checks it.
	if err := os.MkdirAll(filepath.Join(root, "mappings"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mappings", "job-9.json"), []byte(`{"job_id":"job-9"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := NewSpool(root)
		if err != nil {
			t.Fatal(err)
		}
		b, err := s.fsys.ReadFile(filepath.Join(root, "run-events", name))
		if err != nil || !bytes.Equal(b, original) {
			t.Fatalf("receipt lost in migration: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "events"), 0700); err == nil {
		t.Fatal("old writer can recreate mixed event directory")
	}
}

func TestEventLayoutRefusesUnknownAndForeignCollision(t *testing.T) {
	for _, kind := range []string{"unknown", "collision"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			if err := os.MkdirAll(filepath.Join(root, "events"), 0700); err != nil {
				t.Fatal(err)
			}
			name, receipt := runEventsRecord("source-alpha", "evt-1")
			original := receipt
			if kind == "unknown" {
				original = []byte(`{"future_schema":1}`)
			} else {
				if err := os.MkdirAll(filepath.Join(root, "run-events"), 0700); err != nil {
					t.Fatal(err)
				}
				_, other := runEventsRecord("source-beta", "evt-2")
				if err := os.WriteFile(filepath.Join(root, "run-events", name), other, 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "events", name)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewSpool(root); err == nil {
				t.Fatal("unrecognized or conflicting state silently migrated")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatal("rejected migration modified source")
			}
		})
	}
}

func TestEventLayoutMigrationCrashResume(t *testing.T) {
	for _, step := range []string{"planned", "copied", "moved", "fenced"} {
		t.Run(step, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "spool")
			legacy := filepath.Join(root, "events")
			target := filepath.Join(root, "run-events")
			for _, dir := range []string{legacy, target} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			name, data := runEventsRecord("job-1", "evt-1")
			sum := sha256.Sum256(data)
			if err := atomicJSON(filepath.Join(root, "event-layout.json"), eventLayout{Version: 2, Files: []eventMove{{Name: name, Directory: "run-events", Digest: hex.EncodeToString(sum[:])}}}); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "mappings"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "mappings", "job-1.json"), []byte(`{"job_id":"job-1"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if step == "planned" || step == "copied" {
				if err := os.WriteFile(filepath.Join(legacy, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if step != "planned" {
				if err := os.WriteFile(filepath.Join(target, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if step == "fenced" {
				if err := os.Remove(legacy); err != nil {
					t.Fatal(err)
				}
				if err := atomicJSON(legacy, struct {
					Version int `json:"version"`
				}{2}); err != nil {
					t.Fatal(err)
				}
			}
			s, err := NewSpool(root)
			if err != nil {
				t.Fatal(err)
			}
			b, err := s.fsys.ReadFile(filepath.Join(root, "run-events", name))
			if err != nil || !bytes.Equal(b, data) {
				t.Fatalf("resume lost receipt: %v", err)
			}
			if _, err = NewSpool(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}
