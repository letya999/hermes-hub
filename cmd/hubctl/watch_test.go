package main

import (
	"bytes"
	"context"
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

func TestWatchJobTerminal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		status := "running"
		if calls.Add(1) >= 2 {
			status = "completed"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"job_id":"j1","status":%q,"outcome":{"job_id":"j1","run_id":"r1","status":%q}}`, status, status)
	}))
	defer srv.Close()
	var out bytes.Buffer
	err := runWatch(t.Context(), &out, watchOptions{
		Job: "j1", SupervisorURL: srv.URL, SupervisorAuth: "tok",
		Interval: time.Millisecond, Timeout: 10 * time.Second, Client: srv.Client(),
	})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "running") || !strings.Contains(text, "completed") {
		t.Fatalf("transitions missing: %q", text)
	}
	if strings.Count(text, "running") != 1 {
		t.Fatalf("duplicate transition lines: %q", text)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if _, err := time.Parse(time.RFC3339, strings.SplitN(line, " ", 2)[0]); err != nil {
			t.Fatalf("line missing RFC3339 timestamp: %q", line)
		}
	}
}

func TestWatchTerminalStatuses(t *testing.T) {
	for _, terminal := range []string{"failed", "cancelled", "interrupted"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"job_id":"j1","status":%q}`, terminal)
		}))
		var out bytes.Buffer
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		err := runWatch(ctx, &out, watchOptions{
			Job: "j1", SupervisorURL: srv.URL, Interval: time.Millisecond, Client: srv.Client(),
		})
		cancel()
		srv.Close()
		if err != nil {
			t.Fatalf("watch %s: %v", terminal, err)
		}
		if !strings.Contains(out.String(), terminal) {
			t.Fatalf("terminal %s not printed: %q", terminal, out.String())
		}
	}
}

func TestWatchJobFromSpool(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spool, "mappings", "j1.json")
	write := func(status string) {
		b, _ := json.Marshal(commodel.JobMapping{JobID: "j1", Status: status})
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("running")
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runWatch(t.Context(), &out, watchOptions{
			Job: "j1", SpoolDir: spool, Interval: time.Millisecond,
		})
	}()
	time.Sleep(30 * time.Millisecond)
	write("completed")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not exit on terminal mapping")
	}
	text := out.String()
	if !strings.Contains(text, "running") || !strings.Contains(text, "completed") {
		t.Fatalf("transitions missing: %q", text)
	}
}

func TestWatchJobHonorsTimeout(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The job never reaches a terminal status; Timeout is the only exit.
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runWatch(t.Context(), &out, watchOptions{
			Job: "j1", SpoolDir: spool, Interval: time.Millisecond, Timeout: 50 * time.Millisecond,
		})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("watch on a never-terminal job must end on Timeout")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout was not honored — watch ran past the deadline")
	}
}

func TestWatchSpoolMappingAppearsLater(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runWatch(t.Context(), &out, watchOptions{
			Job: "j1", SpoolDir: spool, Interval: time.Millisecond,
		})
	}()
	time.Sleep(30 * time.Millisecond)
	b, _ := json.Marshal(commodel.JobMapping{JobID: "j1", Status: "completed"})
	if err := os.WriteFile(filepath.Join(spool, "mappings", "j1.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not pick up a late mapping")
	}
	if !strings.Contains(out.String(), "completed") {
		t.Fatalf("terminal missing: %q", out.String())
	}
}

func TestWatchScopeTransitions(t *testing.T) {
	var calls atomic.Int32
	states := []string{"starting", "ready", "idle"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/runtimes" {
			w.WriteHeader(404)
			return
		}
		i := int(calls.Add(1)) - 1
		if i >= len(states) {
			i = len(states) - 1
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"principal_id":"u1","context_id":"c1","runtime_id":"r1","state":%q},
			{"principal_id":"u2","context_id":"c2","runtime_id":"r2","state":"ready"}]`, states[i])
	}))
	defer srv.Close()
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runWatch(ctx, &out, watchOptions{
			Scope: "u1", SupervisorURL: srv.URL, Interval: time.Millisecond, Client: srv.Client(),
		})
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watch: %v", err)
	}
	text := out.String()
	for _, s := range states {
		if !strings.Contains(text, "c1 "+s) {
			t.Fatalf("state %s missing: %q", s, text)
		}
	}
	if strings.Contains(text, "u2") || strings.Contains(text, "c2") {
		t.Fatalf("foreign scope leaked: %q", text)
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if _, err := time.Parse(time.RFC3339, strings.SplitN(line, " ", 2)[0]); err != nil {
			t.Fatalf("line missing RFC3339 timestamp: %q", line)
		}
	}
}

func TestWatchScopeFromSpool(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(id, principal, status string) {
		b, _ := json.Marshal(commodel.JobMapping{JobID: id, PrincipalID: principal, Status: status})
		if err := os.WriteFile(filepath.Join(spool, "mappings", id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a1", "u1", "running")
	write("b1", "u2", "running")
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- runWatch(ctx, &out, watchOptions{Scope: "u1", SpoolDir: spool, Interval: time.Millisecond})
	}()
	time.Sleep(30 * time.Millisecond)
	write("a1", "u1", "completed")
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watch scope from spool: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "a1 running") || !strings.Contains(text, "a1 completed") {
		t.Fatalf("spool scope transitions missing: %q", text)
	}
	if strings.Contains(text, "b1") || strings.Contains(text, "u2") {
		t.Fatalf("foreign scope leaked: %q", text)
	}
}

func TestWatchTimeoutFlag(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := run(t.Context(), []string{"watch", "--job", "j1", "--spool", spool, "--interval", "1ms", "--timeout", "30ms"})
	if err == nil {
		t.Fatal("--timeout flag did not bound the watch")
	}
	err = run(t.Context(), []string{"watch", "--scope", "u1", "--spool", spool, "--interval", "1ms", "--timeout", "30ms"})
	if err == nil {
		t.Fatal("--timeout flag did not bound a scope watch")
	}
}

func TestWatchDispatchValidation(t *testing.T) {
	for _, args := range [][]string{
		{"watch"},
		{"watch", "--job", "j1", "--scope", "u1", "--spool", "x"},
		{"watch", "--job", "j1"},
		{"watch", "--job", "j1", "--spool", "x", "extra"},
	} {
		if err := run(t.Context(), args); err == nil {
			t.Fatalf("watch %v: expected validation error", args)
		}
	}
}

func TestWatchDispatchRuns(t *testing.T) {
	spool := t.TempDir()
	if err := os.MkdirAll(filepath.Join(spool, "mappings"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(commodel.JobMapping{JobID: "j1", Status: "completed"})
	if err := os.WriteFile(filepath.Join(spool, "mappings", "j1.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(t.Context(), []string{"watch", "--job", "j1", "--spool", spool, "--interval", "1ms"}); err != nil {
		t.Fatalf("watch dispatch: %v", err)
	}
}

func TestWatchDispatchScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/v1/runtimes" {
			w.WriteHeader(403)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"principal_id":"u1","context_id":"c1","runtime_id":"r1","state":"ready"}]`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	err := run(ctx, []string{"watch", "--scope", "u1", "--supervisor-url", srv.URL,
		"--supervisor-auth", "tok", "--interval", "1ms"})
	if err != nil && err != context.DeadlineExceeded {
		t.Fatalf("watch scope dispatch: %v", err)
	}
}
