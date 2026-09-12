package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/migration"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionSupervisorDrainUsesExactContextAndAuth(t *testing.T) {
	for _, state := range []supervisor.State{supervisor.Stopped, supervisor.Busy, supervisor.Idle} {
		t.Run(string(state), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/runtimes" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("incorrect migration authority")
				}
				json.NewEncoder(w).Encode([]supervisor.Runtime{{ContextID: "bob", State: supervisor.Busy, Leases: 1}, {ContextID: "alice", State: state}})
			}))
			defer api.Close()
			err := checkSupervisorStopped(context.Background(), api.URL, "synthetic", "alice")
			if (err == nil) != (state == supervisor.Stopped) {
				t.Fatalf("state=%s err=%v", state, err)
			}
		})
	}
	if err := checkSupervisorStopped(context.Background(), "http://localhost:1", "", "alice"); err == nil {
		t.Fatal("missing auth accepted")
	}
}

func TestSelectExecutionApplyRefusesActiveComputeAndPreservesPartialRender(t *testing.T) {
	original := executionDockerOutput
	defer func() { executionDockerOutput = original }()
	for _, scenario := range []string{"running", "docker-error", "busy-supervisor", "success", "render-failure", "wrong-user"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "alice")
			spoolDir := filepath.Join(root, "spool")
			if err := stack.Init(dir, "alice"); err != nil {
				t.Fatal(err)
			}
			if _, err := communication.NewSpool(spoolDir); err != nil {
				t.Fatal(err)
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				state := supervisor.Stopped
				if scenario == "busy-supervisor" {
					state = supervisor.Busy
				}
				json.NewEncoder(w).Encode([]supervisor.Runtime{{ContextID: "alice", State: state}})
			}))
			defer api.Close()
			executionDockerOutput = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "run" {
					return json.Marshal(map[string]any{"hermes_pin": migration.HermesMigrationPin, "active_jobs": 0})
				}
				if args[0] == "inspect" {
					return json.Marshal([]map[string]string{{"Source": spoolDir, "Destination": "/data", "Type": "volume"}})
				}
				if args[len(args)-1] == "communication-hub" {
					return []byte("synthetic-stopped-gateway"), nil
				}
				if len(args) != 7 || args[0] != "compose" || args[2] != filepath.Join(dir, "generated", "compose.prod.yaml") {
					t.Fatalf("unscoped Docker drain: %v", args)
				}
				if scenario == "docker-error" {
					return nil, errors.New("unavailable")
				}
				if scenario == "running" {
					return []byte("active-container"), nil
				}
				return nil, nil
			}
			selection := stack.ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: api.URL, NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
			if scenario == "wrong-user" {
				selection.User = "bob"
			}
			sourceRoot := "../.."
			if scenario == "render-failure" {
				sourceRoot = t.TempDir()
			}
			err := selectExecution(context.Background(), dir, sourceRoot, spoolDir, selection, "synthetic", "hermes:test", true)
			if scenario == "success" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe or partial transition claimed success")
			}
			_, statErr := os.Stat(filepath.Join(dir, stack.ExecutionPath("prod")))
			if scenario == "render-failure" {
				if statErr != nil {
					t.Fatal("saved selection unavailable for repair")
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatal("failed preflight changed selection")
			}
		})
	}
}

func TestExecutionCommandsDryRunAndAudit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "alice")
	spool := filepath.Join(root, "spool")
	if err := stack.Init(dir, "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := communication.NewSpool(spool); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"execution-audit", "--spool", spool, "--user", "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"select-execution", "--dir", dir, "--spool", spool, "--user", "alice", "--execution-mode", "supervisor", "--supervisor-url", "http://localhost:8765", "--native-cron", "disabled", "--compatibility-release", "0.2.0"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"select-execution", "--dir", dir}); err == nil {
		t.Fatal("unverified selection accepted")
	}
}

func TestSelectionChecksActualGatewaySpool(t *testing.T) {
	original := executionDockerOutput
	defer func() { executionDockerOutput = original }()
	spool := t.TempDir()
	for _, scenario := range []string{"own", "foreign", "missing", "ambiguous", "mount-error"} {
		t.Run(scenario, func(t *testing.T) {
			executionDockerOutput = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "compose" {
					if scenario == "missing" {
						return nil, nil
					}
					if scenario == "ambiguous" {
						return []byte("a b"), nil
					}
					return []byte("a"), nil
				}
				if scenario == "mount-error" {
					return nil, errors.New("unavailable")
				}
				source := spool
				if scenario == "foreign" {
					source = filepath.Join(spool, "other")
				}
				return json.Marshal([]map[string]string{{"Source": source, "Destination": "/data", "Type": "volume"}})
			}
			err := checkSelectedSpool(context.Background(), "selected-compose", spool)
			if (err == nil) != (scenario == "own") {
				t.Fatalf("%s: %v", scenario, err)
			}
		})
	}
}
