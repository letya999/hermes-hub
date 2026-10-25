package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestWatchDeliveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		states      []string
		job, status string
		corrupt     bool
	}{
		{"uncertain", []string{"failed"}, "j1", "uncertain", false},
		{"uncertain-before-pending", []string{"pending", "failed"}, "j1", "uncertain", false},
		{"delivered", []string{"done"}, "j1", "delivered", false},
		{"foreign", []string{"pending"}, "other", "", false},
		{"corrupt", []string{"pending"}, "j1", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spool := t.TempDir()
			if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(spool, "mappings", "j1.json"), []byte(`{"job_id":"j1","status":"completed"}`), 0600); err != nil {
				t.Fatal(err)
			}
			for _, state := range tc.states {
				dir := filepath.Join(spool, "outbox", state)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(commodel.Delivery{ID: state, JobID: tc.job})
				if tc.corrupt {
					body = []byte("{")
				}
				if err := os.WriteFile(filepath.Join(dir, state+".json"), body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := runWatch(t.Context(), &out, watchOptions{Job: "j1", SpoolDir: spool, Interval: time.Millisecond, Timeout: 100 * time.Millisecond})
			if tc.corrupt {
				if err == nil {
					t.Error("unreadable delivery state reported complete")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != "" && !strings.Contains(out.String(), "delivery "+tc.status) {
				t.Errorf("missing delivery outcome: %q", out.String())
			}
			if tc.status == "" && strings.Contains(out.String(), "delivery ") {
				t.Error("foreign delivery affected watched job")
			}
		})
	}
}
