package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"strings"
	"testing"
)

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
	foundHome, foundExec := false, false
	for _, call := range fake.createCalls() {
		for _, arg := range call {
			if strings.HasPrefix(arg, cellHomeGuest+":rw,exec,") {
				foundHome = true
			}
			if strings.Contains(arg, "target="+cellHomeGuest) {
				t.Fatal("home bind mounted")
			}
		}
	}
	for _, call := range fake.execCalls {
		if api.Contains(call, "HOME="+cellHomeGuest) && api.Contains(call, "PATH="+cellHomeGuest+"/.local/bin:"+cellHomeGuest+"/.cargo/bin:"+cellHomeGuest+"/go/bin:/usr/local/bin:/usr/bin:/bin:"+cellToolsetGuest) {
			foundExec = true
		}
	}
	if !foundHome || !foundExec {
		t.Fatal("private install home or execution PATH missing")
	}
	fake.inspectDrift = func(_ string, container *genericContainer) {
		for i := range container.Mounts {
			if container.Mounts[i].Destination == cellHomeGuest {
				container.Mounts[i].Type = "bind"
			}
		}
	}
	req.Principal, req.ContextID = "alice", "ctx-alice"
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("home drift accepted: %v", err)
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

func TestPackageHomeMustBePresentAndCannotBeSharedTmpfs(t *testing.T) {
	c, fake := cellFixture(t, func(cfg *api.CLIControllerConfig) { cfg.AllowPackageInstall = true })
	req := cliReq("rg", cellExecution())
	req.InstallPackages, req.Lifecycle = true, api.CLILifecycleBinding
	resp, err := c.execCLI(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	cell := c.registry.cells[cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, "", "sha256:"+strings.Repeat("a", 64), nil)+"|install-packages"]
	container := *fake.containers[resp.Receipt.CellID]
	var mounts []genericMount
	for _, mount := range container.Mounts {
		if mount.Destination != cellHomeGuest && mount.Destination != cellUserTmpGuest {
			mounts = append(mounts, mount)
		}
	}
	container.Mounts = mounts
	if got := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); got != "home-mount" {
		t.Fatalf("missing private home accepted: %s", got)
	}
	container.HostConfig.Tmpfs = map[string]string{cellHomeGuest: "rw,exec"}
	if got := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); got != "" {
		t.Fatalf("containerd private home rejected: %s", got)
	}
	cell.installPackages = false
	if got := cellProfileProblem(container, cell, c.config.SeccompProfile, c.config.StateRoot, true); got != "tmpfs:"+cellHomeGuest {
		t.Fatalf("non-install cell carries executable home: %s", got)
	}
}
