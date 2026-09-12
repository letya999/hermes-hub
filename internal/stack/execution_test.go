package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionSelectionStrictOwnershipAndCorruptState(t *testing.T) {
	dir := t.TempDir()
	if _, found, err := ReadExecution(dir, "prod", "alice"); err != nil || found {
		t.Fatal("absent selection did not keep compatibility")
	}
	selection := ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "supervisor", SupervisorURL: "http://localhost:8765", NativeCron: "disabled", CompatibilityRelease: "0.2.0"}
	for _, scenario := range []string{"valid", "compatibility", "user", "environment", "corrupt", "oversized", "directory"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(dir, ExecutionPath("prod"))
			os.Remove(path)
			value := selection
			if scenario == "compatibility" {
				value.Mode, value.SupervisorURL = "legacy", ""
			}
			if scenario == "user" {
				value.User = "bob"
			}
			if scenario == "environment" {
				value.Environment = "dev"
			}
			body, _ := json.Marshal(value)
			if scenario == "corrupt" {
				body = []byte("{")
			}
			if scenario == "oversized" {
				body = make([]byte, 16*1024+1)
			}
			if scenario == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			loaded, found, err := ReadExecution(dir, "prod", "alice")
			if scenario == "valid" || scenario == "compatibility" {
				if err != nil || !found {
					t.Fatal(err)
				}
				if scenario == "compatibility" && loaded.Mode != "static" {
					t.Fatal("compatibility record restored retired executor")
				}
			} else if err == nil {
				t.Fatal("unsafe selection accepted")
			}
			os.Remove(path)
		})
	}
}

func TestExecutionSelectionDoesNotBroadenPolicyAndKeepsUnmigratedClock(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "prod", Timezone: "UTC", Features: []string{"workspace", "telegram"}}
	before := PolicyVersion(s)
	s.ExecutionMode, s.SupervisorURL = "supervisor", "http://localhost:8765"
	if PolicyVersion(s) != before {
		t.Fatal("executor routing changed queued job policy")
	}
	selection := ExecutionSelection{Schema: 1, User: "alice", Environment: "prod", Mode: "static", NativeCron: "unmigrated", CompatibilityRelease: "0.2.0"}
	if err := selection.Validate(); err != nil {
		t.Fatal(err)
	}
	s.ExecutionMode, s.SupervisorURL, s.NativeCron = "static", "", "unmigrated"
	service := RuntimeService(s, "", t.TempDir())
	if service["command"].([]string)[0] != "serve" {
		t.Fatal("unmigrated native clock not resident")
	}
	s.Features, s.NativeCron = []string{"workspace"}, "disabled"
	if RuntimeService(s, "", t.TempDir())["command"].([]string)[0] != "idle" {
		t.Fatal("CLI-only context started resident Hermes")
	}
	s.Features = append(s.Features, "telegram")
	if RuntimeService(s, "", t.TempDir())["command"].([]string)[0] != "serve" {
		t.Fatal("static communication lost native Gateway")
	}
	selection.SupervisorURL = "http://localhost:8765"
	if err := selection.Validate(); err == nil {
		t.Fatal("legacy selection could route to supervisor")
	}
	selection.Mode = "supervisor"
	selection.NativeCron = "guess"
	if err := selection.Validate(); err == nil {
		t.Fatal("ambiguous native cron selection accepted")
	}
}
