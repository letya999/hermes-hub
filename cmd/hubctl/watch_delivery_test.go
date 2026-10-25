package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestWatchCompletedBeforeOutboxRecord(t *testing.T) {
	spool := t.TempDir()
	for _, dir := range []string{"pending", "done"} {
		if err := os.MkdirAll(filepath.Join(spool, "outbox", dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 3 {
			body, _ := json.Marshal(commodel.Delivery{ID: "reply", JobID: "j1"})
			if err := os.WriteFile(filepath.Join(spool, "outbox", "done", "reply.json"), body, 0600); err != nil {
				t.Error(err)
			}
		}
		fmt.Fprint(w, `{"status":"completed"}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := runWatch(t.Context(), &out, watchOptions{Job: "j1", SpoolDir: spool, SupervisorURL: srv.URL,
		Client: srv.Client(), Interval: time.Millisecond, Timeout: 3 * time.Second, DeliveryGrace: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 3 || !strings.Contains(out.String(), "delivery delivered") {
		t.Fatalf("watch exited inside the completion-to-outbox gap: calls=%d output=%q", calls.Load(), out.String())
	}
}

func TestWatchTerminalWithoutAnyReply(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "outbox", "done"), 0700); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"completed"}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := runWatch(t.Context(), &out, watchOptions{Job: "j1", SpoolDir: spool, SupervisorURL: srv.URL,
		Client: srv.Client(), Interval: time.Millisecond, Timeout: 3 * time.Second, DeliveryGrace: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "completed") || strings.Contains(out.String(), "delivery") {
		t.Fatalf("a reply-less job must end cleanly after the grace window: %q", out.String())
	}
}

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
