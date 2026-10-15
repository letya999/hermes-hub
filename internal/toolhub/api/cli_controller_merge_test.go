package api

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validCLIConfig(t *testing.T) *CLIControllerConfig {
	t.Helper()
	dir := t.TempDir()
	cellinit := filepath.Join(dir, "cellinit")
	if err := os.WriteFile(cellinit, []byte("bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	within := ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1024, CPUMillis: 100, MemoryMiB: 64, MaxPIDs: 4, Egress: []string{"api.example.io"}}
	return &CLIControllerConfig{
		ToolsImage:    "hermes-tools@" + digest,
		CellInit:      cellinit,
		AllowedEgress: []string{"api.example.io"},
		Ceiling:       within,
		Approved: []CLIApproval{
			{DefinitionID: "def-1", DefinitionVersion: "1.0.0", Command: "ripgrep", Execution: within},
			{DefinitionID: "def-2", DefinitionVersion: "1.0.0", Command: "/app/tool", Image: "img", Digest: digest, Execution: within},
		},
		UserCommands: []string{"jq"},
		Toolboxes:    map[string][]string{"box-1": {"member@" + digest}},
	}
}

func TestValidateCLIConfigContract(t *testing.T) {
	cfg := validCLIConfig(t)
	if err := ValidateCLIConfig(cfg); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if err := ValidateCLIConfig(nil); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name   string
		mutate func(*CLIControllerConfig)
	}{
		{"image", func(c *CLIControllerConfig) { c.ToolsImage = "bad img!" }},
		{"cellinit missing", func(c *CLIControllerConfig) { c.CellInit = "/nonexistent-cellinit-xyz" }},
		{"cellinit relative", func(c *CLIControllerConfig) { c.CellInit = "cellinit" }},
		{"runtime", func(c *CLIControllerConfig) { c.Runtime = "firecracker" }},
		{"egress empty", func(c *CLIControllerConfig) { c.AllowedEgress = nil }},
		{"egress host", func(c *CLIControllerConfig) { c.AllowedEgress = []string{"BAD HOST!"} }},
		{"environment", func(c *CLIControllerConfig) { c.Environment = "dev env" }},
		{"approvals empty", func(c *CLIControllerConfig) { c.Approved = nil; c.UserCommands = nil }},
		{"approval command", func(c *CLIControllerConfig) { c.Approved[0].Command = "../escape" }},
		{"approval image", func(c *CLIControllerConfig) { c.Approved[1].Digest = "not-a-digest" }},
		{"approval ceiling", func(c *CLIControllerConfig) { c.Approved[0].Execution.TimeoutSeconds = 6000 }},
		{"user command", func(c *CLIControllerConfig) { c.UserCommands = []string{"/abs/path"} }},
		{"toolbox member", func(c *CLIControllerConfig) { c.Toolboxes = map[string][]string{"box-1": {"img:latest"}} }},
		{"pool bounds", func(c *CLIControllerConfig) { c.PoolSize = 17 }},
		{"warm bounds", func(c *CLIControllerConfig) { c.WarmMaxAgeSeconds = 8 * 24 * 3600 }},
	}
	for _, tc := range bad {
		c := validCLIConfig(t)
		tc.mutate(c)
		if err := ValidateCLIConfig(c); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", tc.name, err)
		}
	}
}

func TestPlanApprovedShapes(t *testing.T) {
	cfg := validCLIConfig(t)
	digest := "sha256:" + strings.Repeat("ab", 32)
	within := cfg.Ceiling

	// exact approved tuple
	plan := ControllerPlan{DefinitionID: "def-1", DefinitionVersion: "1.0.0", Execution: within}
	if !PlanApproved(cfg, plan, "ripgrep") {
		t.Fatal("approved tuple rejected")
	}
	if PlanApproved(cfg, plan, "ripgrep2") {
		t.Fatal("wrong command approved")
	}
	// digest-pinned artifact plan: absolute guest command + matching digest
	artifact := ControllerPlan{DefinitionID: "def-9", DefinitionVersion: "9.9.9", Image: "img", Digest: digest}
	if !PlanApproved(cfg, artifact, "/app/tool") {
		t.Fatal("artifact plan rejected")
	}
	if PlanApproved(cfg, artifact, "relative/tool") || PlanApproved(cfg, artifact, "/app/tool; rm -rf /") {
		t.Fatal("unsafe artifact command")
	}
	artifact.Digest = "sha256:short"
	if PlanApproved(cfg, artifact, "/app/tool") {
		t.Fatal("bad digest approved")
	}
	// catalog command plan (no image) admitted by name from UserCommands
	catalog := ControllerPlan{DefinitionID: "def-3", DefinitionVersion: "1.0.0"}
	if !PlanApproved(cfg, catalog, "jq") {
		t.Fatal("user command rejected")
	}
	if PlanApproved(cfg, catalog, "sh") {
		t.Fatal("unlisted command approved")
	}
}
