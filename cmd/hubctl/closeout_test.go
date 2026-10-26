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
	"github.com/letya999/hermes-hub/internal/supervisor"
)

func TestRejectedManifestDoesNotCreateSpool(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "manifest.yaml")
	if err := os.WriteFile(manifest, []byte("space:\n  workspace:\n    invalid_field: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	spool := filepath.Join(root, "absent-spool")
	if _, err := applyFile(spool, "", "me", manifest, initTestSpace(t)); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatalf("rejected manifest created spool: %v", err)
	}
}

func TestWatchWaitsForDelivery(t *testing.T) {
	spool := t.TempDir()
	for _, dir := range []string{"pending", "done"} {
		if err := os.MkdirAll(filepath.Join(spool, "outbox", dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(commodel.Delivery{ID: "reply", JobID: "j1", Text: "never-print-this-result"})
	pending := filepath.Join(spool, "outbox", "pending", "reply.json")
	if err := os.WriteFile(pending, body, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/jobs/j1" {
			t.Error("unexpected watch endpoint")
			w.WriteHeader(404)
			return
		}
		if calls.Add(1) == 2 {
			if err := os.Rename(pending, filepath.Join(spool, "outbox", "done", "reply.json")); err != nil {
				t.Error(err)
			}
		}
		fmt.Fprint(w, `{"status":"completed"}`)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := runWatch(t.Context(), &out, watchOptions{Job: "j1", SpoolDir: spool, SupervisorURL: srv.URL, Client: srv.Client(), Interval: time.Millisecond, Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() < 2 || !strings.Contains(out.String(), "delivery pending") || !strings.Contains(out.String(), "delivery delivered") {
		t.Errorf("watch exited before delivery: calls=%d output=%q", calls.Load(), out.String())
	}
	if strings.Contains(out.String(), "never-print") {
		t.Error("watch disclosed response content")
	}
}

func TestRevertClearsCanary(t *testing.T) {
	dir := t.TempDir()
	mustWriteRolloutPins(t, dir, supervisor.RolloutPins{Envs: map[string]supervisor.RolloutEnv{"prod": {
		Current:  supervisor.RolloutPin{Image: "img:bad", SmokedAt: time.Now()},
		Previous: &supervisor.RolloutPin{Image: "img:good", SmokedAt: time.Now()},
		Canary:   &supervisor.CanaryPin{Image: "img:broken", ContextID: "me", SetAt: time.Now()},
	}}})
	if err := runImage(t.Context(), &bytes.Buffer{}, dir, "prod", smokeOK, []string{"revert"}); err != nil {
		t.Fatal(err)
	}
	pins, err := supervisor.ReadRolloutPins(dir)
	if err != nil || pins.Envs["prod"].Current.Image != "img:good" || pins.Envs["prod"].Canary != nil {
		t.Fatalf("rollback left a broken override: %+v %v", pins, err)
	}
}
