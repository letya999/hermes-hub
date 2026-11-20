package toolhub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// TestPackageInstallPrivateCell: the operator/stateless/tier gates are pure
// admission and unchanged; the private-home isolation itself moved into the
// worker spec spawn-controller builds (the claim carries the flag, the
// server mounts the tmpfs home — the spec assertions live in its suite).
func TestPackageInstallPrivateCell(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle, req.InstallPackages = api.CLILifecycleBinding, true
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("operator gate: %v", err)
	}
	c.config.CLI.AllowPackageInstall = true
	for _, lifecycle := range []string{api.CLILifecycleSharedPool, api.CLILifecycleToolbox} {
		bad := req
		bad.Lifecycle = lifecycle
		if _, err := c.execCLI(context.Background(), bad); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("shared gate: %v", err)
		}
	}
	bad := req
	bad.Stateless = true
	if _, err := c.execCLI(context.Background(), bad); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("stateless gate: %v", err)
	}
	installed, err := c.execCLI(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.execCLI(context.Background(), req)
	if err != nil || installed.Receipt.CellID != again.Receipt.CellID {
		t.Fatalf("private warm reuse: %v", err)
	}
	req.InstallPackages = false
	isolated, err := c.execCLI(context.Background(), req)
	if err != nil || isolated.Receipt.CellID == installed.Receipt.CellID {
		t.Fatalf("non-install cell reused install state: %v", err)
	}
	req.InstallPackages, req.Principal, req.ContextID = true, "bob", "ctx-bob"
	bob, err := c.execCLI(context.Background(), req)
	if err != nil || bob.Receipt.CellID == installed.Receipt.CellID {
		t.Fatalf("cross-principal reuse: %v", err)
	}
	// The install flag reached the claim and the exec env carries the
	// private home + extended PATH — the tmpfs itself is server-side.
	fake.mu.Lock()
	var installExec bool
	for _, call := range fake.execs {
		if call.Env["HOME"] == cellHomeGuest && strings.HasPrefix(call.Env["PATH"], cellHomeGuest+"/.local/bin:") && call.Env["TMPDIR"] == cellUserTmpGuest {
			installExec = true
		}
	}
	var flagged bool
	for _, claim := range fake.claims {
		if claim.InstallPackages {
			flagged = true
		}
	}
	fake.mu.Unlock()
	if !installExec || !flagged {
		t.Fatal("private install claim flag or exec env missing")
	}
}

func TestPackageInstallDefinitionContract(t *testing.T) {
	w := cliSpecWorkload(map[string]any{"install_packages": true, "workspace_scope": "none"}, false)
	if !w.InstallPackages {
		t.Fatal("install_packages was dropped")
	}
	d := api.CLICatalogDefinitions()[0]
	d.Workload = w
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*api.ToolDefinition){func(d *api.ToolDefinition) { d.Workload.Stateless = true }, func(d *api.ToolDefinition) { d.Workload.Lifecycle = api.CLILifecycleSharedPool }, func(d *api.ToolDefinition) { d.Transport = api.AgentTools }} {
		bad := d
		mutate(&bad)
		if err := bad.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("invalid install definition: %v", err)
		}
	}
}
