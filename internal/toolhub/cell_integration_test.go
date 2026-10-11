//go:build integration

package toolhub

// Real-Docker canary for the sandbox-cell boundary. The unit suite proves the
// controller only mounts /cellinit, /tmp and one workspace bind; this test
// proves the claim against an actual daemon: inside the cell there is no
// /state, no broker material, no other principal's workspace and no docker
// socket — the four audit holes stay closed in production, not just in mocks.
//
//	HUB_CLI_TOOLS_IMAGE=hermes-hub-cli-tools:test go test -tags integration -run TestCLICellRealDocker ./internal/toolhub
//
// Requires: docker daemon, the cli-tools image (just docker-check builds it)
// and a Linux-capable `go` toolchain to cross-build the cellinit helper.
import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCLICellRealDocker(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tools := envOr("HUB_CLI_TOOLS_IMAGE", "hermes-hub-cli-tools:test")
	if out, err := localCommand(ctx, "docker", "image", "inspect", tools); err != nil {
		t.Skipf("image %s unavailable: %v (%s)", tools, err, out)
	}

	root := t.TempDir()
	// The controller seeds this binary into the state root and bind-mounts it
	// into every cell; it must be a Linux binary no matter the host OS.
	cellinit := filepath.Join(root, "cellinit")
	build := exec.CommandContext(ctx, "go", "build", "-o", cellinit, "github.com/letya999/hermes-hub/cmd/cellinit")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cross-build cellinit: %v (%s)", err, out)
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte(`{"defaultAction":"SCMP_ACT_ALLOW"}`), 0600); err != nil {
		t.Fatal(err)
	}

	c := &genericController{
		config: GenericControllerConfig{
			StateRoot:      root,
			SeccompProfile: seccomp,
			IdleTTLSeconds: 60,
			MaxActive:      4,
			CLI: &CLIControllerConfig{
				ToolsImage:    tools,
				CellInit:      cellinit,
				AllowedEgress: []string{"*"},
				Ceiling:       ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64},
				UserCommands:  []string{"ls", "cat", "sleep", "sh", "uv"},
			},
		},
		command:     localCommand,
		commandExit: localCommandExit,
		workloads:   map[string]genericWorkload{},
		registry:    newCellRegistry(),
	}
	t.Cleanup(func() { c.releaseAllCells(context.Background(), "test-done") })

	req := func(command string, args ...string) cliExecRequest {
		return cliExecRequest{
			Plan: controllerPlan{WorkloadID: "w-cell-int", DefinitionID: "cli-probe", DefinitionVersion: "1.0.0",
				Execution: ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 65536, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 64, Egress: []string{"127.0.0.1"}}},
			Command: command, Args: args,
			Principal: "alice", ContextID: "ctx-alice", BindingID: "bind-alice",
		}
	}

	// Audit hole 1+3: nothing of the controller's filesystem or the docker
	// socket exists inside the cell. `ls -d` prints each path that EXISTS as
	// a bare name line — "cannot access ... No such file" errors mean absent.
	probed := []string{"/state", "/var/run/docker.sock", "/run/broker-materialized",
		"/run/broker-secrets", "/run/docker.sock"}
	resp, err := c.execCLI(ctx, req("ls", append([]string{"-d"}, probed...)...))
	if err != nil {
		t.Fatalf("probe exec denied: %v", err)
	}
	if resp.ExitCode == 0 {
		t.Fatalf("all probed paths exist inside the cell: %q", resp.Output)
	}
	for _, line := range strings.Split(resp.Output, "\n") {
		line = strings.TrimSpace(line)
		for _, p := range probed {
			if line == p {
				t.Fatalf("cell can see host surface %s: %q", p, resp.Output)
			}
		}
	}
	// The receipt claims the cell identity, runtime and isolation scopes.
	if resp.Receipt.CellID == "" || resp.Receipt.Runtime == "" || len(resp.Receipt.Isolation) != 3 || !resp.Receipt.Enforced {
		t.Fatalf("receipt=%+v", resp.Receipt)
	}

	// Audit hole 4: network is none — the loopback-only plan earned no
	// interface beyond lo.
	resp, err = c.execCLI(ctx, req("cat", "/proc/net/dev"))
	if err != nil {
		t.Fatalf("net probe denied: %v", err)
	}
	for _, line := range strings.Split(resp.Output, "\n") {
		if strings.Contains(line, ":") && !strings.HasPrefix(strings.TrimSpace(line), "lo:") {
			t.Fatalf("cell has a real interface: %s", line)
		}
	}

	// The mount table holds only the helper, tmpfs and scratch /work.
	resp, err = c.execCLI(ctx, req("cat", "/proc/mounts"))
	if err != nil {
		t.Fatalf("mount probe denied: %v", err)
	}
	for _, banned := range []string{"docker.sock", "broker", "/state", "/spaces"} {
		if strings.Contains(resp.Output, banned) {
			t.Fatalf("cell mounts carry %s:\n%s", banned, resp.Output)
		}
	}
	// Execute a newly created binary only in an operator-approved private
	// home; neither a different caller nor the runtime home can supply it.
	c.config.CLI.AllowPackageInstall = true
	install := req("sh", "-c", `mkdir -p "$HOME/.local/bin"; printf '#!/bin/sh\necho private-install\n' > "$HOME/.local/bin/probe"; chmod +x "$HOME/.local/bin/probe"; probe`)
	install.InstallPackages, install.Lifecycle = true, CLILifecycleBinding
	resp, err = c.execCLI(ctx, install)
	if err != nil || resp.ExitCode != 0 || !strings.Contains(resp.Output, "private-install") {
		t.Fatalf("private executable home: %v %+v", err, resp)
	}
	install.Args = []string{"-c", "probe"}
	resp, err = c.execCLI(ctx, install)
	if err != nil || resp.ExitCode != 0 {
		t.Fatalf("private installation did not survive warm reuse: %v %+v", err, resp)
	}
	install.Principal, install.ContextID, install.BindingID = "bob", "ctx-bob", "bind-bob"
	resp, err = c.execCLI(ctx, install)
	if err != nil || resp.ExitCode == 0 {
		t.Fatalf("installation leaked to another caller: %v %+v", err, resp)
	}
	if proxy := os.Getenv("HUB_CLI_REGISTRY_PROXY_IMAGE"); proxy != "" {
		c.config.CLI.ToolsImage = envOr("HUB_CLI_REGISTRY_TOOLS_IMAGE", "hermes-hub:0.3.0-dev")
		c.config.CLI.ProxyImage = proxy
		c.config.CLI.Ceiling.Egress = []string{"pypi.org", "files.pythonhosted.org"}
		install = req("uv", "tool", "install", "--python", "/opt/hermes/.venv/bin/python3", "--no-python-downloads", "ruff==0.11.13")
		install.Plan.Execution.Egress = append([]string(nil), c.config.CLI.Ceiling.Egress...)
		install.InstallPackages, install.Lifecycle, install.BindingID = true, CLILifecycleBinding, "bind-registry"
		resp, err = c.execCLI(ctx, install)
		if err != nil || resp.ExitCode != 0 {
			t.Fatalf("PyPI install through allowlist: %v %+v", err, resp)
		}
		install.Command, install.Args = "sh", []string{"-c", "ruff --version"}
		resp, err = c.execCLI(ctx, install)
		if err != nil || resp.ExitCode != 0 || !strings.Contains(resp.Output, "ruff 0.11.13") {
			t.Fatalf("installed registry binary: %v %+v", err, resp)
		}
	}
	// Context cancellation reaches the daemon: a cell must die with the call.
	if runtime.GOOS != "windows" {
		short, stop := context.WithTimeout(ctx, 2*time.Second)
		defer stop()
		_, err = c.execCLI(short, req("sleep", "60"))
		if err == nil || errors.Is(err, ErrInvalid) {
			t.Fatalf("cancelled exec: %v", err)
		}
	}
}
