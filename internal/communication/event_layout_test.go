package communication

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestEventLayoutMigratesCursorAndFencesOldWriter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(filepath.Join(root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	name := spoolFileID("paused-source") + ".json"
	original := []byte(`{"schedule_id":"paused-source","cursor":"resume-42","seen":["one"]}`)
	if err := os.WriteFile(filepath.Join(root, "events", name), original, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := NewSpool(root)
		if err != nil {
			t.Fatal(err)
		}
		st, err := s.EventStateOf("paused-source")
		if err != nil || st.Cursor != "resume-42" || len(st.Seen) != 1 {
			t.Fatalf("cursor lost: %+v %v", st, err)
		}
		b, err := os.ReadFile(filepath.Join(root, "event-state", name))
		if err != nil || !bytes.Equal(b, original) {
			t.Fatal("migration changed cursor bytes")
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
			name := spoolFileID("source-alpha") + ".json"
			original := []byte(`{"schedule_id":"source-alpha","cursor":"alpha-42"}`)
			if kind == "unknown" {
				original = []byte(`{"future_schema":1}`)
			} else {
				if err := os.MkdirAll(filepath.Join(root, "event-state"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "event-state", name), []byte(`{"schedule_id":"source-beta","cursor":"beta-99"}`), 0600); err != nil {
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
			target := filepath.Join(root, "event-state")
			for _, dir := range []string{legacy, target} {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			name := spoolFileID("source-alpha") + ".json"
			data := []byte(`{"schedule_id":"source-alpha","cursor":"resume-42"}`)
			sum := sha256.Sum256(data)
			if err := atomicJSON(filepath.Join(root, "event-layout.json"), eventLayout{Version: 2, Files: []eventMove{{Name: name, Directory: "event-state", Digest: hex.EncodeToString(sum[:])}}}); err != nil {
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
			st, err := s.EventStateOf("source-alpha")
			if err != nil || st.Cursor != "resume-42" {
				t.Fatalf("resume lost state: %+v %v", st, err)
			}
			if _, err = NewSpool(root); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMissingInitializedCursorShowsGap(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.writeEventStateLocked("alpha", EventState{ScheduleID: "alpha", Cursor: "cursor-42"}); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(s.eventPath("alpha")); err != nil {
		t.Fatal(err)
	}
	st, err := s.EventStateOf("alpha")
	if err == nil || !st.Degraded || st.GapCount < 1 {
		t.Fatalf("lost cursor silently reset: %+v %v", st, err)
	}
	if err = os.RemoveAll(filepath.Join(s.root, "event-state")); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSpool(s.root); err == nil {
		t.Fatal("lost initialized event directory silently recreated")
	}
}
