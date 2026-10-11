package toolhub

// Cell-model coverage: the fake docker CLI reflects created containers back
// through inspect the way the daemon does, so create→inspect→start→exec→rm,
// warm reuse, pool claims, drift kills and the HTTP contract all run against
// the same profile checks production relies on.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// cellDockerFake answers the docker CLI surface cells use. Created containers
// are reflected back through inspect; the exec hook supplies tool output.
type cellDockerFake struct {
	mu         sync.Mutex
	containers map[string]*genericContainer
	images     map[string]string
	runtimes   []string
	networks   map[string]bool
	volumes    map[string]bool
	calls      [][]string
	execCalls  [][]string
	// inspectDrift mutates the inspected record for the named container —
	// the daemon-side lie a drifted or hostile cell would produce.
	inspectDrift func(name string, container *genericContainer)
	// exec runs the in-cell argv (after the container name). Nil means
	// every exec exits 0 with no output.
	exec func(argv []string, sink io.Writer) (int, error)
	// initExec, when set, handles argv that starts with cellInitGuest —
	// lets a test make a canary verb fail.
	initExec func(argv []string) (int, error)
	// failWhen fails one docker argv; failDocker fails them all.
	failWhen   func(args []string) error
	failDocker error
}

func newCellDockerFake() *cellDockerFake {
	return &cellDockerFake{
		containers: map[string]*genericContainer{},
		images: map[string]string{
			"hermes-hub-cli-tools:test":  "sha256:" + strings.Repeat("a", 64),
			"hermes-hub-cell-proxy:test": "sha256:" + strings.Repeat("b", 64),
		},
		runtimes: []string{"runc", "runsc"},
		networks: map[string]bool{},
		volumes:  map[string]bool{},
	}
}

func (f *cellDockerFake) run(_ context.Context, binary string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if binary != "docker" {
		return nil, fmt.Errorf("unexpected binary %s", binary)
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	if f.failDocker != nil {
		return nil, f.failDocker
	}
	if f.failWhen != nil {
		if err := f.failWhen(args); err != nil {
			return nil, err
		}
	}
	switch args[0] {
	case "image":
		// image inspect <ref> --format {{.Id}}
		if len(args) >= 2 && args[1] == "inspect" && len(args) >= 3 {
			if id, ok := f.images[args[2]]; ok {
				return []byte(id + "\n"), nil
			}
			return nil, fmt.Errorf("Error: No such image: %s", args[2])
		}
	case "info":
		body := map[string]map[string]string{}
		for _, rt := range f.runtimes {
			body[rt] = map[string]string{"path": rt}
		}
		out, _ := json.Marshal(body)
		return out, nil
	case "network":
		if len(args) >= 2 && args[1] == "create" {
			f.networks[args[len(args)-1]] = true
			return []byte("net\n"), nil
		}
		if len(args) >= 2 && args[1] == "rm" {
			for _, name := range args[2:] {
				delete(f.networks, name)
			}
			return nil, nil
		}
		if len(args) >= 2 && args[1] == "connect" {
			if member, ok := f.containers[args[3]]; ok {
				member.NetworkSettings.Networks[args[2]] = struct{ IPAddress string }{IPAddress: "172.31.0.9"}
			}
			return nil, nil
		}
		return nil, nil
	case "volume":
		if len(args) >= 2 && args[1] == "create" {
			f.volumes[args[len(args)-1]] = true
			return []byte(args[len(args)-1] + "\n"), nil
		}
		if len(args) >= 2 && args[1] == "rm" {
			for _, name := range args[2:] {
				delete(f.volumes, name)
			}
			return nil, nil
		}
		return nil, nil
	case "create":
		member, err := f.recordCreate(args[1:])
		if err != nil {
			return nil, err
		}
		f.containers[member.name] = member.container
		return []byte(member.name + "\n"), nil
	case "inspect":
		if len(args) < 2 {
			return nil, fmt.Errorf("inspect needs a name")
		}
		name := args[1]
		container, ok := f.containers[name]
		if !ok {
			return nil, fmt.Errorf("Error: No such container: %s", name)
		}
		copy := *container
		if f.inspectDrift != nil && strings.HasPrefix(name, "cli-") && !strings.HasSuffix(name, "-proxy") && !strings.HasSuffix(name, "-cred") {
			f.inspectDrift(name, &copy)
		}
		// The --format proxyIP variant returns the networks object only.
		for i, arg := range args {
			if arg == "--format" && i+1 < len(args) && strings.Contains(args[i+1], "NetworkSettings") {
				return json.Marshal(copy.NetworkSettings.Networks)
			}
		}
		return json.Marshal([]genericContainer{copy})
	case "start":
		for _, name := range args[1:] {
			member, ok := f.containers[name]
			if !ok {
				return nil, fmt.Errorf("Error: No such container: %s", name)
			}
			member.State.Running = true
		}
		return nil, nil
	case "rm":
		for _, name := range args[1:] {
			if strings.HasPrefix(name, "-") {
				continue
			}
			delete(f.containers, name)
		}
		return nil, nil
	case "exec":
		// Non-streaming exec used by the proxy readiness probe. The fake has
		// no real sockets, so a running container counts as listening.
		i := 1
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			i++
		}
		if i >= len(args) {
			return nil, fmt.Errorf("exec without container name")
		}
		member, ok := f.containers[args[i]]
		if !ok || !member.State.Running {
			return nil, fmt.Errorf("Error: container %s is not running", args[i])
		}
		return nil, nil
	case "cp":
		return nil, nil
	case "logs":
		return nil, nil
	case "wait":
		return []byte("0\n"), nil
	}
	return nil, fmt.Errorf("unexpected docker args %v", args)
}

// recordedCreate separates the container record from its name so the map
// keying stays explicit.
type recordedCreate struct {
	name      string
	container *genericContainer
}

func (f *cellDockerFake) recordCreate(args []string) (recordedCreate, error) {
	container := &genericContainer{}
	container.NetworkSettings.Networks = map[string]struct{ IPAddress string }{}
	name := ""
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			name = args[i]
		case "--user":
			i++
			container.Config.User = args[i]
		case "--network":
			i++
			container.HostConfig.NetworkMode = args[i]
			container.NetworkSettings.Networks[args[i]] = struct{ IPAddress string }{IPAddress: "172.31.0.2"}
		case "--runtime":
			i++
			container.HostConfig.Runtime = args[i]
		case "--workdir":
			i++
		case "--read-only":
			container.HostConfig.ReadonlyRootfs = true
		case "--privileged":
			container.HostConfig.Privileged = true
		case "--cap-drop":
			i++
			container.HostConfig.CapDrop = append(container.HostConfig.CapDrop, args[i])
		case "--cap-add":
			i++
			container.HostConfig.CapAdd = append(container.HostConfig.CapAdd, args[i])
		case "--security-opt":
			i++
			container.HostConfig.SecurityOpt = append(container.HostConfig.SecurityOpt, args[i])
		case "--tmpfs":
			i++
			target, _, _ := strings.Cut(args[i], ":")
			container.Mounts = append(container.Mounts, genericMount{Type: "tmpfs", Destination: target, RW: true})
		case "--mount":
			i++
			mount := genericMount{RW: true}
			for _, kv := range strings.Split(args[i], ",") {
				key, value, _ := strings.Cut(kv, "=")
				switch key {
				case "type":
					mount.Type = value
				case "source":
					mount.Source, mount.Name = value, value
				case "target":
					mount.Destination = value
				case "readonly":
					mount.RW = false
				}
			}
			container.Mounts = append(container.Mounts, mount)
		case "--label":
			i++
			key, value, _ := strings.Cut(args[i], "=")
			if container.Config.Labels == nil {
				container.Config.Labels = map[string]string{}
			}
			container.Config.Labels[key] = value
		case "--cpus":
			i++
			cpus, _ := strconv.ParseFloat(args[i], 64)
			container.HostConfig.NanoCpus = int64(cpus * 1000000000)
		case "--memory", "--memory-swap":
			flag := args[i]
			i++
			if flag == "--memory" {
				memory, _ := strconv.ParseInt(strings.TrimSuffix(args[i], "m"), 10, 64)
				container.HostConfig.Memory = memory * 1048576
			}
		case "--pids-limit":
			i++
			container.HostConfig.PidsLimit, _ = strconv.ParseInt(args[i], 10, 64)
		case "--entrypoint":
			i++
		case "--env", "-e":
			i++
			container.Config.Env = append(container.Config.Env, args[i])
		default:
			if strings.HasPrefix(args[i], "-") {
				return recordedCreate{}, fmt.Errorf("unhandled create flag %s", args[i])
			}
			// The first positional is the image; everything after it is the
			// container's own argv, which may legitimately contain flags.
			positionals = append(positionals, args[i:]...)
			i = len(args)
		}
	}
	if name == "" || len(positionals) == 0 {
		return recordedCreate{}, fmt.Errorf("create without name or image: %v", args)
	}
	container.Image = positionals[0]
	container.Config.Image = positionals[0]
	return recordedCreate{name: name, container: container}, nil
}

// commandExit covers the exec/write/clean/procs/sweep surface; cellinit
// subcommands succeed by default, tool argv goes to the exec hook.
func (f *cellDockerFake) commandExit(ctx context.Context, sink io.Writer, binary string, args ...string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if binary != "docker" || len(args) == 0 || args[0] != "exec" {
		return -1, fmt.Errorf("unexpected exec seam %s %v", binary, args)
	}
	f.execCalls = append(f.execCalls, append([]string(nil), args...))
	if f.failDocker != nil {
		return -1, f.failDocker
	}
	// Skip exec options: -e K=V pairs and -w workdir, then the cell name.
	i := 1
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		i += 2
	}
	if i >= len(args) {
		return -1, fmt.Errorf("exec without container name")
	}
	argv := args[i+1:]
	if len(argv) > 0 && argv[0] == cellInitGuest {
		if f.initExec != nil {
			return f.initExec(argv)
		}
		return 0, nil
	}
	if f.exec != nil {
		return f.exec(argv, sink)
	}
	return 0, nil
}

// execArgs returns the in-cell argv of every recorded docker exec.
func (f *cellDockerFake) execArgv() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := [][]string{}
	for _, call := range f.execCalls {
		i := 1
		for i < len(call) && strings.HasPrefix(call[i], "-") {
			i += 2
		}
		out = append(out, call[i:])
	}
	return out
}

func (f *cellDockerFake) createCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := [][]string{}
	for _, call := range f.calls {
		if call[0] == "create" {
			out = append(out, call)
		}
	}
	return out
}

func (f *cellDockerFake) removed(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.containers[name]
	return !ok
}

func cellExecution() ExecutionPolicy {
	return ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}}
}

// cellFixture builds a controller on the fake daemon: real state root, a
// shipped cellinit file, and the operator envelope the render layer emits.
func cellFixture(t *testing.T, mutate func(*CLIControllerConfig)) (*genericController, *cellDockerFake) {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	cellinit := filepath.Join(binDir, "cellinit")
	if err := os.WriteFile(cellinit, []byte("cellinit"), 0700); err != nil {
		t.Fatal(err)
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &CLIControllerConfig{
		ToolsImage:    "hermes-hub-cli-tools:test",
		CellInit:      cellinit,
		ProxyImage:    "hermes-hub-cell-proxy:test",
		AllowedEgress: []string{"github.com", "gitlab.com", "bitbucket.org", "api.github.com", "codeberg.org", "127.0.0.1"},
		Ceiling:       cellExecution(),
		Approved:      []CLIApproval{{DefinitionID: "cli-git-ls-remote", DefinitionVersion: "1.0.1", Command: "git", Execution: ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"github.com", "gitlab.com", "bitbucket.org"}}}},
		UserCommands:  []string{"rg"},
		PoolSize:      2,
	}
	if mutate != nil {
		mutate(cfg)
	}
	fake := newCellDockerFake()
	c := &genericController{
		config:      GenericControllerConfig{StateRoot: root, SeccompProfile: seccomp, CLI: cfg, MaxActive: 8, IdleTTLSeconds: 300},
		command:     fake.run,
		commandExit: fake.commandExit,
		workloads:   map[string]genericWorkload{},
		registry:    newCellRegistry(),
	}
	return c, fake
}

// cliReq builds an owner-catalog exec request; egress defaults to loopback so
// the cell lands on network none unless the test declares otherwise.
func cliReq(command string, execution ExecutionPolicy) cliExecRequest {
	return cliExecRequest{
		Plan:      controllerPlan{WorkloadID: "w-cli-alice", DefinitionID: "cli-owner", DefinitionVersion: "1.0.0", Command: command, Execution: execution},
		Command:   command,
		Principal: "alice", ContextID: "ctx-alice", BindingID: "bind-1",
	}
}

func TestCLIExecEphemeralLifecycle(t *testing.T) {
	c, fake := cellFixture(t, nil)
	response, err := c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatalf("ephemeral exec denied: %v", err)
	}
	receipt := response.Receipt
	if receipt.CellID == "" || receipt.Runtime != "runc" || receipt.State != "running" || !receipt.Enforced || len(receipt.Isolation) != 3 {
		t.Fatalf("receipt=%+v", receipt)
	}
	if !fake.removed(receipt.CellID) {
		t.Fatalf("ephemeral cell %s survived", receipt.CellID)
	}
	// Lifecycle order: image inspect, create, inspect, start, canary write,
	// inspect, exec, rm — prove the controller did not run the tool inside
	// this process.
	var ops []string
	fake.mu.Lock()
	for _, call := range fake.calls {
		ops = append(ops, call[0])
	}
	fake.mu.Unlock()
	joined := strings.Join(ops, ",")
	if !strings.Contains(joined, "create") || !strings.Contains(joined, "start") {
		t.Fatalf("cell lifecycle missing create/start: %v", ops)
	}
	// The tool argv ran inside the cell through docker exec, not locally.
	argv := fake.execArgv()
	found := false
	for _, call := range argv {
		if len(call) >= 2 && call[1] == "rg" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool never execed in a cell: %v", argv)
	}
	// Default egress is loopback-only → the cell must be network none.
	var create []string
	for _, call := range fake.createCalls() {
		for i, arg := range call {
			if arg == "--name" && i+1 < len(call) && strings.HasPrefix(call[i+1], "cli-") {
				create = call
			}
		}
	}
	if !containsArgPair(create, "--network", "none") {
		t.Fatalf("cell without reviewed egress was not network none: %v", create)
	}
	// A shared runtime home would let terminal-installed binaries cross the
	// immutable cell boundary. Only the controller helper may bind here.
	for i, arg := range create {
		if arg == "--mount" && i+1 < len(create) && !strings.Contains(create[i+1], "target=/cellinit,readonly") {
			t.Fatalf("unexpected cell bind (runtime home/state must stay out): %s", create[i+1])
		}
	}
}

func containsArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestCLIExecPlanDenials(t *testing.T) {
	c, _ := cellFixture(t, nil)
	valid := cliReq("rg", cellExecution())
	deny := func(name string, req cliExecRequest, want error) {
		t.Helper()
		if _, err := c.execCLI(context.Background(), req); !errors.Is(err, want) {
			t.Fatalf("%s: expected %v, got %v", name, want, err)
		}
	}
	bad := valid
	bad.Plan.DefinitionID = "no spaces"
	deny("identity", bad, ErrInvalid)
	bad = valid
	bad.Plan.Execution.MaxPIDs = c.config.CLI.Ceiling.MaxPIDs + 1
	deny("over-ceiling", bad, ErrUnauthorized)
	bad = valid
	bad.Plan.Execution.Egress = []string{"evil.example.com"}
	deny("egress", bad, ErrUnauthorized)
	bad = valid
	bad.Command = "git"
	deny("unapproved command", bad, ErrUnauthorized)
	bad = valid
	bad.Command = "rg;rm"
	deny("shell command", bad, ErrInvalid)
	bad = valid
	bad.Plan.WorkspacePath = "/state"
	deny("container-mcp field", bad, ErrInvalid)
	bad = valid
	bad.Env = []string{"HTTPS_PROXY=http://evil.example"}
	deny("caller proxy env", bad, ErrUnauthorized)
	bad = valid
	bad.Env = []string{"PATH=/evil"}
	deny("caller PATH", bad, ErrUnauthorized)
	bad = valid
	bad.Lifecycle = CLILifecycleTask
	deny("task without job", bad, ErrInvalid)
	bad = valid
	bad.Lifecycle = "bogus"
	deny("unknown lifecycle", bad, ErrInvalid)
	bad = valid
	bad.Lifecycle = CLILifecycleSharedPool // missing stateless marker
	deny("stateful pool cell", bad, ErrUnauthorized)
	bad = valid
	bad.Brokered = []cliBrokeredCred{{EnvName: "TOKEN", Host: "api.example.com", Value: "v"}}
	// Brokered creds are allowed by shape — but the proxy image is configured
	// so this request passes validation and exercises the cred-proxy path.
	if _, err := c.execCLI(context.Background(), bad); err != nil {
		t.Fatalf("brokered credential exec denied: %v", err)
	}
}

func TestCLIExecCellProfileDrift(t *testing.T) {
	drifts := map[string]func(*genericContainer){
		"extra mount": func(m *genericContainer) {
			m.Mounts = append(m.Mounts, genericMount{Type: "bind", Destination: "/var/run/docker.sock"})
		},
		"writable root": func(m *genericContainer) { m.HostConfig.ReadonlyRootfs = false },
		"cap add":       func(m *genericContainer) { m.HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"host pid":      func(m *genericContainer) { m.HostConfig.PidMode = "host" },
		"privileged":    func(m *genericContainer) { m.HostConfig.Privileged = true },
		"root user":     func(m *genericContainer) { m.Config.User = "root" },
		"host network":  func(m *genericContainer) { m.HostConfig.NetworkMode = "host" },
		"image swap":    func(m *genericContainer) { m.Config.Image = "sha256:" + strings.Repeat("f", 64) },
		"label drop":    func(m *genericContainer) { delete(m.Config.Labels, "hermes-hub.cell") },
		"seccomp off":   func(m *genericContainer) { m.HostConfig.SecurityOpt = []string{"no-new-privileges=true"} },
	}
	for name, mutate := range drifts {
		c, fake := cellFixture(t, nil)
		fake.inspectDrift = func(_ string, container *genericContainer) { mutate(container) }
		if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
			t.Fatalf("%s: drifted cell admitted: %v", name, err)
		}
	}
}

func TestCLIExecEgressSidecars(t *testing.T) {
	c, fake := cellFixture(t, nil)
	execution := cellExecution()
	execution.Egress = []string{"github.com"}
	response, err := c.execCLI(context.Background(), cliReq("rg", execution))
	if err != nil {
		t.Fatalf("egress exec denied: %v", err)
	}
	// The cell joined its own internal network and a Squid sidecar was
	// created, configured through docker cp and bridged for egress.
	var cellCreate, proxyCreate []string
	for _, call := range fake.createCalls() {
		for i, arg := range call {
			if arg == "--name" && i+1 < len(call) {
				if strings.HasPrefix(call[i+1], "cli-") && !strings.HasSuffix(call[i+1], "-proxy") {
					cellCreate = call
				}
				if strings.HasSuffix(call[i+1], "-proxy") {
					proxyCreate = call
				}
			}
		}
	}
	if cellCreate == nil || proxyCreate == nil {
		fake.mu.Lock()
		t.Fatalf("cell or proxy create missing: %v", fake.calls)
	}
	var cellNet string
	for i, arg := range cellCreate {
		if arg == "--network" {
			cellNet = cellCreate[i+1]
		}
	}
	if cellNet == "" || cellNet == "none" || cellNet == "bridge" {
		t.Fatalf("egress cell not on its internal network: %v", cellCreate)
	}
	if !containsArgPair(proxyCreate, "--network", cellNet) {
		t.Fatalf("proxy not on the cell network: %v", proxyCreate)
	}
	// The exec env carries the squid proxy address, never a secret.
	var envPairs []string
	fake.mu.Lock()
	for _, call := range fake.execCalls {
		for i := 1; i < len(call); i++ {
			if call[i] == "-e" && i+1 < len(call) {
				envPairs = append(envPairs, call[i+1])
			}
		}
	}
	fake.mu.Unlock()
	joined := strings.Join(envPairs, "\n")
	if !strings.Contains(joined, "HTTPS_PROXY=http://172.31.0.2:3128") {
		t.Fatalf("cell exec missing proxy env: %v", envPairs)
	}
	if response.Receipt.CellID == "" {
		t.Fatal("egress exec returned no cell receipt")
	}
}

func TestCLIExecWarmReuseCanaryAndRelease(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := len(fake.createCalls()); got != 1 {
		t.Fatalf("warm cell was not reused: creates=%d", got)
	}
	c.mu.Lock()
	metrics := c.cells().metrics
	c.mu.Unlock()
	if metrics.creates != 1 || metrics.reuses != 1 {
		t.Fatalf("metrics=%+v", metrics)
	}
	// Release through the controller path kills the warm cell.
	c.releaseCells(context.Background(), cliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "test"})
	c.mu.Lock()
	left := len(c.cells().cells)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("release left %d warm cells", left)
	}
	// Canary failure on a claimed cell kills it and creates a fresh one.
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	cell := c.cells().cells[cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, "", "sha256:"+strings.Repeat("a", 64), nil)]
	c.mu.Unlock()
	if cell == nil {
		t.Fatal("warm cell missing")
	}
	// One-shot drift: the claimed cell fails its canary, the replacement is clean.
	var drifted atomic.Bool
	fake.inspectDrift = func(_ string, container *genericContainer) {
		if drifted.CompareAndSwap(false, true) {
			container.HostConfig.Privileged = true
		}
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	metrics = c.cells().metrics
	c.mu.Unlock()
	if metrics.canaryFails != 1 || metrics.creates != 3 {
		t.Fatalf("drifted warm cell was not killed and replaced: %+v", metrics)
	}
	if !fake.removed(cell.name) {
		t.Fatalf("drifted cell %s survived", cell.name)
	}
}

func TestCLIExecPoolClaimRelease(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = CLILifecycleSharedPool
	req.Stateless = true
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	metrics := c.cells().metrics
	c.mu.Unlock()
	if metrics.creates != 1 || metrics.poolHits != 1 || metrics.poolClaims != 1 || metrics.poolMisses != 1 {
		t.Fatalf("pool metrics=%+v", metrics)
	}
	// Pool cells stay alive for reuse.
	if len(fake.createCalls()) != 1 {
		t.Fatalf("pool creates=%d", len(fake.createCalls()))
	}
	// A drifted pool cell is quarantined, not reused.
	c.mu.Lock()
	var pooled *cliCell
	for _, list := range c.cells().pool {
		if len(list) > 0 {
			pooled = list[0]
		}
	}
	c.mu.Unlock()
	var drifted atomic.Bool
	fake.inspectDrift = func(_ string, container *genericContainer) {
		if drifted.CompareAndSwap(false, true) {
			container.Config.User = "root"
		}
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !fake.removed(pooled.name) {
		t.Fatalf("dirty pool cell %s survived", pooled.name)
	}
	c.mu.Lock()
	metrics = c.cells().metrics
	c.mu.Unlock()
	if metrics.creates != 2 {
		t.Fatalf("dirty pool cell was not replaced: %+v", metrics)
	}
}

func TestCLIExecPoolStarvationBound(t *testing.T) {
	c, fake := cellFixture(t, func(cfg *CLIControllerConfig) { cfg.PoolSize = 1; cfg.PoolClaimWaitSeconds = 1 })
	req := cliReq("rg", cellExecution())
	req.Lifecycle = CLILifecycleSharedPool
	req.Stateless = true
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Claim the only cell out of band; a second caller waits then fails closed.
	c.mu.Lock()
	var image string
	for key := range c.cells().pool {
		image = key
	}
	c.mu.Unlock()
	held := c.claimPoolCell(image)
	if held == nil {
		t.Fatal("pool claim lost the cell")
	}
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, ErrIsolation) {
		t.Fatalf("starved pool admitted: %v", err)
	}
	c.mu.Lock()
	metrics := c.cells().metrics
	live := len(c.cells().pool[image]) + c.cells().claimedN[image]
	c.mu.Unlock()
	if metrics.poolExhaustions != 1 || metrics.claimWaitNanos <= 0 || live != 1 {
		t.Fatalf("pool bound metrics=%+v live=%d", metrics, live)
	}
	// Releasing the held claim frees capacity for the next caller; the exec
	// slot drains the way execPoolOnce does it on a real claim.
	<-held.execSlot
	if !c.returnPoolCell(held) {
		t.Fatal("held cell refused return")
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("released pool denied: %v", err)
	}
	if len(fake.createCalls()) != 1 {
		t.Fatalf("bounded pool created %d cells", len(fake.createCalls()))
	}
}

func TestCLIExecRuntimeTiers(t *testing.T) {
	c, fake := cellFixture(t, func(cfg *CLIControllerConfig) { cfg.Runtime = "runsc" })
	response, err := c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatalf("runsc cell denied: %v", err)
	}
	if response.Receipt.Runtime != "runsc" {
		t.Fatalf("receipt runtime=%q", response.Receipt.Runtime)
	}
	var create []string
	for _, call := range fake.createCalls() {
		create = call
	}
	if !containsArgPair(create, "--runtime", "runsc") {
		t.Fatalf("create lacked --runtime runsc: %v", create)
	}
	// A daemon without the tier denies.
	c, fake = cellFixture(t, func(cfg *CLIControllerConfig) { cfg.Runtime = "kata" })
	fake.runtimes = []string{"runc"}
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing kata runtime admitted: %v", err)
	}
	// An unknown tier is rejected by config validation, not just at create.
	c, _ = cellFixture(t, func(cfg *CLIControllerConfig) { cfg.Runtime = "bogus" })
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown runtime admitted: %v", err)
	}
}

func TestCLIExecWorkspaceScopes(t *testing.T) {
	c, _ := cellFixture(t, func(cfg *CLIControllerConfig) { cfg.SpacesRoot = t.TempDir() })
	// Binding scope binds an existing dir under the state root.
	workspace := filepath.Join(c.config.StateRoot, "per-user", "alice")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	req := cliReq("rg", cellExecution())
	req.Workspace = cliWorkspace{Scope: "binding", Path: workspace, Access: "rw"}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("binding scope denied: %v", err)
	}
	// Outside the state root denies before any docker call.
	bad := cliReq("rg", cellExecution())
	bad.Workspace = cliWorkspace{Scope: "binding", Path: t.TempDir(), Access: "rw"}
	if _, err := c.execCLI(context.Background(), bad); !errors.Is(err, ErrIsolation) {
		t.Fatalf("outside workspace admitted: %v", err)
	}
	// Missing dir denies.
	missing := cliReq("rg", cellExecution())
	missing.Workspace = cliWorkspace{Scope: "binding", Path: filepath.Join(c.config.StateRoot, "gone")}
	if _, err := c.execCLI(context.Background(), missing); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing workspace admitted: %v", err)
	}
	// Principal scope resolves under the configured spaces root.
	if err := os.MkdirAll(filepath.Join(c.config.CLI.SpacesRoot, "alice", "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	principal := cliReq("rg", cellExecution())
	principal.Workspace = cliWorkspace{Scope: "principal"}
	if _, err := c.execCLI(context.Background(), principal); err != nil {
		t.Fatalf("principal scope denied: %v", err)
	}
	// A path supplied without a scope is a contract violation.
	none := cliReq("rg", cellExecution())
	none.Workspace = cliWorkspace{Scope: "none", Path: workspace}
	if _, err := c.execCLI(context.Background(), none); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unscoped workspace path admitted: %v", err)
	}
	// Pool cells may not carry a workspace at all.
	pool := cliReq("rg", cellExecution())
	pool.Lifecycle = CLILifecycleSharedPool
	pool.Stateless = true
	pool.Workspace = cliWorkspace{Scope: "binding", Path: workspace}
	if _, err := c.execCLI(context.Background(), pool); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("pool workspace admitted: %v", err)
	}
}

func TestCLIExecArtifactAndToolboxPlans(t *testing.T) {
	c, fake := cellFixture(t, nil)
	// A digest-pinned artifact plan runs its own image, command is a guest path.
	req := cliReq("/usr/bin/mytool", cellExecution())
	req.Plan.Image = "ghcr.io/example/mytool"
	req.Plan.Digest = "sha256:" + strings.Repeat("9", 64)
	fake.images["ghcr.io/example/mytool@"+req.Plan.Digest] = "sha256:" + strings.Repeat("9", 64)
	response, err := c.execCLI(context.Background(), req)
	if err != nil {
		t.Fatalf("artifact plan denied: %v", err)
	}
	if response.Receipt.ImageDigest != req.Plan.Digest {
		t.Fatalf("artifact receipt digest=%q", response.Receipt.ImageDigest)
	}
	// A bare-name command on an artifact plan is a contract violation.
	bad := cliReq("mytool", cellExecution())
	bad.Plan.Image = "ghcr.io/example/mytool"
	bad.Plan.Digest = "sha256:" + strings.Repeat("9", 64)
	if _, err := c.execCLI(context.Background(), bad); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bare artifact command admitted: %v", err)
	}
	// Toolbox cells mount the pinned member images at /tools/<i>.
	c, fake = cellFixture(t, func(cfg *CLIControllerConfig) {
		cfg.Toolboxes = map[string][]string{"searchbox": {"ghcr.io/example/toolbox@sha256:" + strings.Repeat("8", 64)}}
	})
	fake.images["ghcr.io/example/toolbox@sha256:"+strings.Repeat("8", 64)] = "sha256:" + strings.Repeat("8", 64)
	req = cliReq("/bin/search", cellExecution())
	req.Plan.Image = "ghcr.io/example/toolbox"
	req.Plan.Digest = "sha256:" + strings.Repeat("8", 64)
	req.Lifecycle = CLILifecycleToolbox
	req.ToolboxID = "searchbox"
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("toolbox exec denied: %v", err)
	}
	argv := fake.execArgv()
	found := false
	for _, call := range argv {
		if len(call) >= 2 && strings.HasPrefix(call[1], "/tools/0/") {
			found = true
		}
	}
	if !found {
		t.Fatalf("toolbox command did not resolve through /tools mount: %v", argv)
	}
	// The toolbox cell base is the tools image — members mount read-only
	// under /tools/<i>; a scratch member base would leave dynamic binaries
	// without an ELF interpreter in the cell rootfs.
	fake.mu.Lock()
	var toolboxBase string
	for name, member := range fake.containers {
		if member.Config.Labels["hermes-hub.cell.lifecycle"] == CLILifecycleToolbox {
			toolboxBase = member.Config.Image
			_ = name
		}
	}
	fake.mu.Unlock()
	if toolboxBase != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("toolbox cell base=%q, want tools image", toolboxBase)
	}
	// A request for a member image outside the toolbox denies.
	fake.images["ghcr.io/example/other@sha256:"+strings.Repeat("8", 64)] = "sha256:" + strings.Repeat("7", 64)
	req.Plan.Image = "ghcr.io/example/other"
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign toolbox member admitted: %v", err)
	}
	// Unknown toolbox ids deny.
	req.Plan.Image = "ghcr.io/example/toolbox"
	req.ToolboxID = "missing"
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unknown toolbox admitted: %v", err)
	}
}

func TestCLIExecOutputLimitAndExitCode(t *testing.T) {
	c, fake := cellFixture(t, nil)
	fake.exec = func(argv []string, sink io.Writer) (int, error) {
		_, _ = sink.Write([]byte(strings.Repeat("x", 300000)))
		return 0, nil
	}
	response, err := c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != "output-limit" || response.Receipt.CellID == "" || len(response.Output) > 262144 {
		t.Fatalf("overflow response=%+v", response)
	}
	// Non-zero tool exit surfaces as the exit code, not an isolation error.
	fake.exec = func(argv []string, sink io.Writer) (int, error) {
		_, _ = sink.Write([]byte("not found"))
		return 2, nil
	}
	response, err = c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil || response.ExitCode != 2 || response.Output != "not found" {
		t.Fatalf("exit response=%+v err=%v", response, err)
	}
	// Docker-level failure is an isolation error.
	fake.failDocker = errors.New("daemon gone")
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
		t.Fatalf("daemon failure admitted: %v", err)
	}
}

func TestCLIExecHTTPContract(t *testing.T) {
	c, _ := cellFixture(t, nil)
	server := httptest.NewServer(c.handler("tok-tok-tok-tok-tok-tok-tok-toktoktok"))
	defer server.Close()
	post := func(path, body, token string) *http.Response {
		request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response
	}
	// Unauthenticated calls deny.
	if response := post("/cli-exec", "{}", ""); response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /cli-exec: %d", response.StatusCode)
	}
	// Unknown fields deny before execution.
	if response := post("/cli-exec", `{"hack":true}`, "tok-tok-tok-tok-tok-tok-tok-toktoktok"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown-field /cli-exec: %d", response.StatusCode)
	}
	// Trailing content denies.
	if response := post("/cli-exec", "{}\n{}", "tok-tok-tok-tok-tok-tok-tok-toktoktok"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("trailing-content /cli-exec: %d", response.StatusCode)
	}
	// Bounded-cli plans no longer belong on /admit.
	if response := post("/admit", `{"command":"rg"}`, "tok-tok-tok-tok-tok-tok-tok-toktoktok"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("cli plan on /admit: %d", response.StatusCode)
	}
	// /cli-release accepts the bounded request shape.
	if response := post("/cli-release", `{"principal_id":"alice","binding_id":"bind-1"}`, "tok-tok-tok-tok-tok-tok-tok-toktoktok"); response.StatusCode != http.StatusNoContent {
		t.Fatalf("/cli-release: %d", response.StatusCode)
	}
	if response := post("/cli-release", `{"principal_id":"alice","extra":1}`, "tok-tok-tok-tok-tok-tok-tok-toktoktok"); response.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown-field /cli-release: %d", response.StatusCode)
	}
}

func TestCLICellCleanupRotation(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	key := cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, "", "sha256:"+strings.Repeat("a", 64), nil)
	c.mu.Lock()
	cell := c.cells().cells[key]
	cell.uses = 1 << 20 // past any configured max-reuse bound
	c.mu.Unlock()
	c.cleanupCells(context.Background(), time.Now())
	if !fake.removed(cell.name) {
		t.Fatalf("max-reuse cell survived cleanup")
	}
	// Idle TTL rotates an untouched cell.
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	cell = c.cells().cells[key]
	cell.lastUse = time.Now().Add(-time.Hour)
	c.mu.Unlock()
	c.cleanupCells(context.Background(), time.Now())
	if !fake.removed(cell.name) {
		t.Fatalf("idle cell survived cleanup")
	}
	// Shutdown kills every cell, warm or pooled.
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	poolReq := cliReq("rg", cellExecution())
	poolReq.Lifecycle, poolReq.Stateless = CLILifecycleSharedPool, true
	if _, err := c.execCLI(context.Background(), poolReq); err != nil {
		t.Fatal(err)
	}
	c.releaseAllCells(context.Background(), "shutdown")
	c.mu.Lock()
	left := len(c.cells().cells) + len(c.cells().pool)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("shutdown left %d cells", left)
	}
	fake.mu.Lock()
	remaining := len(fake.containers)
	fake.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("shutdown left %d containers", remaining)
	}
}

func TestCellWorkspacePrincipalManagedLayout(t *testing.T) {
	c, _ := cellFixture(t, nil)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alice", "managed", "dev", "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "alice", "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bob", "workspace"), 0755); err != nil {
		t.Fatal(err)
	}
	req := func(p string) cliExecRequest {
		return cliExecRequest{Principal: p, Workspace: cliWorkspace{Scope: "principal"}}
	}
	// Managed principal with the env configured resolves managed/<env>/workspace.
	c.config.CLI.SpacesRoot, c.config.CLI.Environment = root, "dev"
	got, rw, err := c.cellWorkspaceBind(req("alice"))
	if err != nil || rw {
		t.Fatalf("managed bind: %v rw=%v", err, rw)
	}
	want := filepath.Join("alice", "managed", "dev", "workspace")
	if !strings.HasSuffix(filepath.ToSlash(got), filepath.ToSlash(want)) {
		t.Fatalf("managed workspace not picked: %q", got)
	}
	// A principal without a managed dir falls back to the top-level workspace.
	got, _, err = c.cellWorkspaceBind(req("bob"))
	if err != nil || !strings.HasSuffix(filepath.ToSlash(got), "bob/workspace") {
		t.Fatalf("fallback workspace not picked: %v %q", err, got)
	}
	// No environment configured: the unmanaged layout is always used.
	c.config.CLI.Environment = ""
	got, _, err = c.cellWorkspaceBind(req("alice"))
	if err != nil || !strings.HasSuffix(filepath.ToSlash(got), "alice/workspace") {
		t.Fatalf("unmanaged workspace not picked: %v %q", err, got)
	}
	// Without a spaces root the scope refuses to guess.
	c.config.CLI.SpacesRoot = ""
	if _, _, err = c.cellWorkspaceBind(req("alice")); !errors.Is(err, ErrIsolation) {
		t.Fatalf("principal scope without spaces root: %v", err)
	}
}

// Squid needs a beat between docker start and its port bind; the probe runs
// inside the proxy because the internal network is unreachable otherwise.
func TestWaitProxyListen(t *testing.T) {
	c, fake := cellFixture(t, nil)
	member := &genericContainer{}
	member.State.Running = true
	fake.containers["p-ready"] = member
	if err := c.waitProxyListen(context.Background(), "p-ready"); err != nil {
		t.Fatalf("running proxy not accepted: %v", err)
	}

	old := proxyListenTimeout
	proxyListenTimeout = 250 * time.Millisecond
	defer func() { proxyListenTimeout = old }()
	if err := c.waitProxyListen(context.Background(), "p-gone"); !errors.Is(err, ErrIsolation) {
		t.Fatalf("dead proxy err=%v, want ErrIsolation", err)
	}
}
