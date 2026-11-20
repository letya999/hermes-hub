package toolhub

// Cell-worker coverage: the executor moved behind contracts/spawn/v1
// (CHG-0336 S6), so these tests drive execCLI against the spawn/v1 fake
// server through a counting client. Admission, lifecycle placement, the
// held-worker registry and the release/drain path are ToolHub's; the
// mount/isolation guarantees moved to spawn-controller's own suite.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// spawnClientSpy wraps the contract fake to count calls and record claims —
// the observability the old docker fake provided on the cell side.
type spawnClientSpy struct {
	spawnv1.Client
	mu       sync.Mutex
	claims   []spawnv1.ClaimWorkerRequest
	execs    []spawnv1.WorkerExecRequest
	renews   int
	releases int
	drains   []spawnv1.DrainWorkersRequest
}

func (s *spawnClientSpy) ClaimWorker(ctx context.Context, space string, in spawnv1.ClaimWorkerRequest) (spawnv1.ClaimedWorker, error) {
	s.mu.Lock()
	s.claims = append(s.claims, in)
	s.mu.Unlock()
	return s.Client.ClaimWorker(ctx, space, in)
}

func (s *spawnClientSpy) RenewWorker(ctx context.Context, id string, in spawnv1.WorkerLeaseRequest) (spawnv1.Worker, error) {
	s.mu.Lock()
	s.renews++
	s.mu.Unlock()
	return s.Client.RenewWorker(ctx, id, in)
}

func (s *spawnClientSpy) ReleaseWorker(ctx context.Context, id string, in spawnv1.WorkerLeaseRequest) error {
	s.mu.Lock()
	s.releases++
	s.mu.Unlock()
	return s.Client.ReleaseWorker(ctx, id, in)
}

func (s *spawnClientSpy) DrainWorkers(ctx context.Context, space string, in spawnv1.DrainWorkersRequest) (int, error) {
	s.mu.Lock()
	s.drains = append(s.drains, in)
	s.mu.Unlock()
	return s.Client.DrainWorkers(ctx, space, in)
}

func (s *spawnClientSpy) ExecWorker(ctx context.Context, id string, in spawnv1.WorkerExecRequest) (spawnv1.WorkerExecResult, error) {
	s.mu.Lock()
	s.execs = append(s.execs, in)
	s.mu.Unlock()
	return s.Client.ExecWorker(ctx, id, in)
}

func (s *spawnClientSpy) counts() (claims, renews, releases int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.claims), s.renews, s.releases
}

// fixtureFake is the test double cellFixture returns: the spawn client spy
// plus the host image table and a failure knob for the docker seam ToolHub
// still uses to resolve image refs (image inspect / info only — create,
// exec and rm for cells moved to spawn-controller).
type fixtureFake struct {
	*spawnClientSpy
	images     map[string]string
	failDocker error
}

func (f *fixtureFake) run(_ context.Context, binary string, args ...string) ([]byte, error) {
	if binary != "docker" {
		return nil, fmt.Errorf("unexpected binary %s", binary)
	}
	if f.failDocker != nil {
		return nil, f.failDocker
	}
	if len(args) >= 3 && args[0] == "image" && args[1] == "inspect" {
		if id, ok := f.images[args[2]]; ok {
			return []byte(id + "\n"), nil
		}
		return nil, fmt.Errorf("Error: No such image: %s", args[2])
	}
	if len(args) >= 1 && args[0] == "info" {
		out, _ := json.Marshal(map[string]map[string]string{"runc": {"path": "runc"}})
		return out, nil
	}
	return nil, fmt.Errorf("toolhub must not run docker %v for cells", args)
}

// cellFixture builds a controller whose spawn client is the in-proc fake —
// the /cli-exec path is exercised end to end minus the network hop. The
// returned fake lets tests count claims, renews and releases and steer the
// image-resolution seam.
func cellFixture(t *testing.T, mutate func(*api.CLIControllerConfig)) (*genericController, *fixtureFake) {
	t.Helper()
	root := t.TempDir()
	cellinit := filepath.Join(root, "bin", "cellinit")
	if err := os.MkdirAll(filepath.Dir(cellinit), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellinit, []byte("cellinit"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &api.CLIControllerConfig{
		ToolsImage:    "hermes-hub-cli-tools:test",
		CellInit:      cellinit,
		ProxyImage:    "hermes-hub-cell-proxy:test",
		AllowedEgress: []string{"github.com", "gitlab.com", "bitbucket.org", "api.github.com", "codeberg.org", "127.0.0.1"},
		Ceiling:       api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}},
		Approved:      []api.CLIApproval{{DefinitionID: "cli-git-ls-remote", DefinitionVersion: "1.0.1", Command: "git", Execution: api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"github.com", "gitlab.com", "bitbucket.org"}}}},
		UserCommands:  []string{"rg"},
	}
	if mutate != nil {
		mutate(cfg)
	}
	f := &fixtureFake{
		spawnClientSpy: &spawnClientSpy{Client: spawnv1.NewInProc(&fake.Server{})},
		images: map[string]string{
			"hermes-hub-cli-tools:test":  "sha256:" + strings.Repeat("a", 64),
			"hermes-hub-cell-proxy:test": "sha256:" + strings.Repeat("b", 64),
		},
	}
	seccomp := filepath.Join(root, "seccomp.json")
	if err := os.WriteFile(seccomp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &genericController{
		config:    GenericControllerConfig{StateRoot: root, SeccompProfile: seccomp, CLI: cfg, MaxActive: 8, IdleTTLSeconds: 300},
		command:   f.run,
		workloads: map[string]genericWorkload{},
		held:      map[string]*heldWorker{},
		spawn:     f.spawnClientSpy,
	}
	return c, f
}

// cliReq builds an owner-catalog exec request; egress defaults to loopback.
func cliReq(command string, execution api.ExecutionPolicy) api.CliExecRequest {
	return api.CliExecRequest{
		Plan:      api.ControllerPlan{WorkloadID: "w-cli-alice", DefinitionID: "cli-owner", DefinitionVersion: "1.0.0", Command: command, Execution: execution},
		Command:   command,
		Principal: "alice", ContextID: "ctx-alice", BindingID: "bind-1",
	}
}

func cellExecution() api.ExecutionPolicy {
	return api.ExecutionPolicy{TimeoutSeconds: 60, OutputBytes: 262144, CPUMillis: 2000, MemoryMiB: 2048, MaxPIDs: 128, Egress: []string{"127.0.0.1"}}
}

// TestToolHubCellsUnavailableWithoutSpawn is the V5 refusal: no
// HUB_SPAWN_URL means no spawn client, CLI cells refuse, and a server
// without worker support refuses the same way — ToolHive MCP is untouched.
func TestToolHubCellsUnavailableWithoutSpawn(t *testing.T) {
	c, f := cellFixture(t, nil)
	c.spawn = nil
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("no-spawn exec: %v, want isolation refusal", err)
	}
	f.Client = spawnv1.NewInProc(&fake.Server{CellsOff: true})
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("cells-off exec: %v, want isolation refusal", err)
	}
}

// TestCLIExecPlanDenials keeps the admission matrix green on the new path —
// every refusal must happen before a worker claim.
func TestCLIExecPlanDenials(t *testing.T) {
	c, spy := cellFixture(t, nil)
	valid := cliReq("rg", cellExecution())
	deny := func(name string, req api.CliExecRequest, want error) {
		t.Helper()
		if _, err := c.execCLI(context.Background(), req); !errors.Is(err, want) {
			t.Fatalf("%s: expected %v, got %v", name, want, err)
		}
	}
	bad := valid
	bad.Plan.DefinitionID = "no spaces"
	deny("identity", bad, api.ErrInvalid)
	bad = valid
	bad.Plan.Execution.MaxPIDs = c.config.CLI.Ceiling.MaxPIDs + 1
	deny("over-ceiling", bad, api.ErrUnauthorized)
	bad = valid
	bad.Plan.Execution.Egress = []string{"evil.example.com"}
	deny("egress", bad, api.ErrUnauthorized)
	bad = valid
	bad.Command = "git"
	deny("unapproved command", bad, api.ErrUnauthorized)
	bad = valid
	bad.Command = "rg;rm"
	deny("shell command", bad, api.ErrInvalid)
	bad = valid
	bad.Plan.WorkspacePath = "/state"
	deny("container-mcp field", bad, api.ErrInvalid)
	bad = valid
	bad.Env = []string{"HTTPS_PROXY=http://evil.example"}
	deny("caller proxy env", bad, api.ErrUnauthorized)
	bad = valid
	bad.Env = []string{"PATH=/evil"}
	deny("caller PATH", bad, api.ErrUnauthorized)
	bad = valid
	bad.Lifecycle = api.CLILifecycleTask
	deny("task without job", bad, api.ErrInvalid)
	bad = valid
	bad.Lifecycle = "bogus"
	deny("unknown lifecycle", bad, api.ErrInvalid)
	bad = valid
	bad.Lifecycle = api.CLILifecycleSharedPool // missing stateless marker
	deny("stateful pool cell", bad, api.ErrUnauthorized)
	bad = valid
	bad.Brokered = []api.CliBrokeredCred{{EnvName: "TOKEN", Host: "api.example.com", Value: "v"}}
	if _, err := c.execCLI(context.Background(), bad); err != nil {
		t.Fatalf("brokered credential exec denied: %v", err)
	}
	if claims, _, _ := spy.counts(); claims != 1 {
		t.Fatalf("denied calls reached the claim path: claims=%d", claims)
	}
}

// TestCLIExecEphemeralWorker: ephemeral calls claim a fresh worker, exec the
// tool argv inside it and release the lease — every single time.
func TestCLIExecEphemeralWorker(t *testing.T) {
	c, spy := cellFixture(t, nil)
	response, err := c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatalf("ephemeral exec denied: %v", err)
	}
	receipt := response.Receipt
	if receipt.CellID == "" || !receipt.Enforced || len(receipt.Isolation) != 3 {
		t.Fatalf("receipt=%+v", receipt)
	}
	claims, _, releases := spy.counts()
	if claims != 1 || releases != 1 {
		t.Fatalf("ephemeral lifecycle claims=%d releases=%d, want 1/1", claims, releases)
	}
	spy.mu.Lock()
	exec := spy.execs[0]
	spy.mu.Unlock()
	if len(exec.Argv) < 1 || exec.Argv[0] != "rg" {
		t.Fatalf("tool argv missing from exec: %v", exec.Argv)
	}
	// The claim identifies alice's worker under the local org with an
	// ephemeral cell key that can never collide with another caller.
	spy.mu.Lock()
	claim := spy.claims[0]
	spy.mu.Unlock()
	if claim.Owner != "alice" || claim.Org != "local" || !strings.HasPrefix(claim.CellKey, "ephemeral|") {
		t.Fatalf("claim identity wrong: %+v", claim)
	}
	// A second call claims again — ephemeral workers are never held.
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); err != nil {
		t.Fatal(err)
	}
	if claims, _, _ := spy.counts(); claims != 2 {
		t.Fatalf("ephemeral reuse happened: claims=%d", claims)
	}
}

// TestCLIExecSharedPoolIsFresh: the cross-user per-image pool is gone — a
// stateless shared-pool call is an ephemeral claim like any other.
func TestCLIExecSharedPoolIsFresh(t *testing.T) {
	c, spy := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleSharedPool
	req.Stateless = true
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	claims, _, releases := spy.counts()
	if claims != 2 || releases != 2 {
		t.Fatalf("pool path held a worker: claims=%d releases=%d", claims, releases)
	}
	c.mu.Lock()
	held := len(c.held)
	c.mu.Unlock()
	if held != 0 {
		t.Fatalf("pool path left %d held workers", held)
	}
}

// TestCLIExecWarmReuseAndRelease: binding-tier calls hold the worker across
// execs via lease renew; /cli-release drains it through the contract.
func TestCLIExecWarmReuseAndRelease(t *testing.T) {
	c, spy := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	claims, renews, _ := spy.counts()
	if claims != 1 || renews != 1 {
		t.Fatalf("warm reuse wrong: claims=%d renews=%d", claims, renews)
	}
	c.mu.Lock()
	metrics := c.workerMetrics
	heldN := len(c.held)
	c.mu.Unlock()
	if metrics.creates != 1 || metrics.reuses != 1 || heldN != 1 {
		t.Fatalf("metrics=%+v held=%d", metrics, heldN)
	}
	c.drainWorkers(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "test"})
	spy.mu.Lock()
	drains := len(spy.drains)
	drain := spy.drains[0]
	spy.mu.Unlock()
	if drains != 1 || drain.BindingID != "bind-1" || drain.Owner != "alice" {
		t.Fatalf("drain=%+v", drain)
	}
	c.mu.Lock()
	heldN = len(c.held)
	c.mu.Unlock()
	if heldN != 0 {
		t.Fatalf("release left %d held workers", heldN)
	}
	// A drain-scoped release leaves other bindings' workers held.
	other := cliReq("rg", cellExecution())
	other.Lifecycle = api.CLILifecycleBinding
	other.BindingID = "bind-2"
	if _, err := c.execCLI(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	c.drainWorkers(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "test"})
	c.mu.Lock()
	heldN = len(c.held)
	c.mu.Unlock()
	if heldN != 1 {
		t.Fatalf("binding-scoped drain removed foreign workers: held=%d", heldN)
	}
}

// TestCLIExecOutputLimit: a tool exceeding its output cap reports
// output-limit with the truncated body — the failure is the tool's.
func TestCLIExecOutputLimit(t *testing.T) {
	c, spy := cellFixture(t, nil)
	srv := &fake.Server{ExecFunc: func(in spawnv1.WorkerExecRequest) (spawnv1.WorkerExecResult, error) {
		return spawnv1.WorkerExecResult{Stdout: make([]byte, int(in.MaxOutput)+16), StdoutTruncated: true}, nil
	}}
	spy.spawnClientSpy.Client = spawnv1.NewInProc(srv)
	response, err := c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatal(err)
	}
	if response.Error != "output-limit" || response.Receipt.CellID == "" || len(response.Output) > 262144 {
		t.Fatalf("overflow response=%+v", response)
	}
	srv.ExecFunc = func(in spawnv1.WorkerExecRequest) (spawnv1.WorkerExecResult, error) {
		return spawnv1.WorkerExecResult{ExitCode: 2, Stdout: []byte("not found")}, nil
	}
	response, err = c.execCLI(context.Background(), cliReq("rg", cellExecution()))
	if err != nil {
		t.Fatal(err)
	}
	if response.ExitCode != 2 || response.Output != "not found" {
		t.Fatalf("nonzero tool exit mishandled: %+v", response)
	}
}

// TestCLIExecToolboxMember: a toolbox call claims with the resolved member
// toolset and the tool argv resolves through /tools/<i>/.
func TestCLIExecToolboxMember(t *testing.T) {
	member := "sha256:" + strings.Repeat("8", 64)
	c, spy := cellFixture(t, func(cfg *api.CLIControllerConfig) {
		cfg.Toolboxes = map[string][]string{"searchbox": {"ghcr.io/example/toolbox@" + member}}
	})
	// The toolbox member ref resolves on the host's image table too.
	spy.images["ghcr.io/example/toolbox@"+member] = member
	req := cliReq("/bin/search", cellExecution())
	req.Plan.Image = "ghcr.io/example/toolbox"
	req.Plan.Digest = member
	req.Lifecycle = api.CLILifecycleToolbox
	req.ToolboxID = "searchbox"
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("toolbox exec denied: %v", err)
	}
	spy.mu.Lock()
	claim := spy.claims[len(spy.claims)-1]
	var execArgv []string
	for _, call := range spy.execs {
		if len(call.Argv) > 0 && call.Argv[0] != cellInitGuest {
			execArgv = call.Argv
		}
	}
	spy.mu.Unlock()
	if len(claim.Toolset) != 1 || claim.Toolset[0] != member {
		t.Fatalf("claim toolset=%v", claim.Toolset)
	}
	if len(execArgv) == 0 || !strings.HasPrefix(execArgv[0], "/tools/0/") {
		t.Fatalf("toolbox argv did not resolve through /tools mount: %v", execArgv)
	}
}

// TestCLIExecEnvSecretChannel: brokered credential values reach the worker
// claim (the sidecar rules channel) but never the exec env or argv — the
// tool sees only the cred-proxy URL.
func TestCLIExecEnvSecretChannel(t *testing.T) {
	c, spy := cellFixture(t, nil)
	secret := "sk-live-" + strings.Repeat("s", 16)
	req := cliReq("rg", cellExecution())
	req.Brokered = []api.CliBrokeredCred{{EnvName: "GH_TOKEN", Host: "api.github.com", Prefix: "Bearer ", Value: secret}}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("brokered exec denied: %v", err)
	}
	spy.mu.Lock()
	claim := spy.claims[0]
	exec := spy.execs[0]
	spy.mu.Unlock()
	if len(claim.Brokered) != 1 || claim.Brokered[0].Value != secret {
		t.Fatal("credential value never reached the worker claim")
	}
	var blob strings.Builder
	_, _ = blob.WriteString(strings.Join(exec.Argv, "\x00"))
	for k, v := range exec.Env {
		_, _ = blob.WriteString(k + "=" + v + "\x00")
	}
	if strings.Contains(blob.String(), secret) {
		t.Fatal("credential value reached exec argv/env")
	}
	got := exec.Env["GH_TOKEN"]
	if !strings.Contains(got, "-cred:3129") {
		t.Fatalf("credential env=%q, want cred-proxy URL", got)
	}
}

// TestCLIExecEgressClaim: an approved-egress plan forwards the allowlist in
// the claim; the exec env carries the squid endpoint the worker reports.
func TestCLIExecEgressClaim(t *testing.T) {
	c, spy := cellFixture(t, nil)
	execution := cellExecution()
	execution.Egress = []string{"github.com"}
	if _, err := c.execCLI(context.Background(), cliReq("rg", execution)); err != nil {
		t.Fatalf("egress exec denied: %v", err)
	}
	spy.mu.Lock()
	claim := spy.claims[0]
	exec := spy.execs[0]
	spy.mu.Unlock()
	if len(claim.Egress) != 1 || claim.Egress[0] != "github.com" {
		t.Fatalf("claim egress=%v", claim.Egress)
	}
	if !strings.HasPrefix(exec.Env["HTTPS_PROXY"], "http://") || !strings.HasSuffix(exec.Env["HTTPS_PROXY"], ":3128") {
		t.Fatalf("exec proxy env=%q", exec.Env["HTTPS_PROXY"])
	}
}
