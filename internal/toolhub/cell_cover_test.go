package toolhub

// Coverage-focused tests for the cell admission surface: every validator
// branch, every egress/sidecar failure cleanup, and the bookkeeping paths
// the happy-path suite does not reach.

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCellImageRefValidation(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	valid := []string{"repo/img@" + digest, "sha256:" + strings.Repeat("e", 64), "local-name:tag"}
	for _, ref := range valid {
		if err := api.ValidCellImageRef(ref); err != nil {
			t.Fatalf("validCellImageRef(%q): %v", ref, err)
		}
	}
	invalid := []string{"", "img@" + digest[:len(digest)-1] + "z", "img@not-a-digest", "name with space:tag", "@sha256:" + strings.Repeat("d", 64), "a..b@c:d"}
	for _, ref := range invalid {
		if err := api.ValidCellImageRef(ref); err == nil {
			t.Fatalf("validCellImageRef(%q) passed", ref)
		}
	}
	if err := api.ValidPinnedImageRef("repo/img@" + digest); err != nil {
		t.Fatalf("validPinnedImageRef: %v", err)
	}
	for _, ref := range []string{"repo/img", "img:tag", "sha256:" + strings.Repeat("e", 64), "img@sha256:short"} {
		if err := api.ValidPinnedImageRef(ref); err == nil {
			t.Fatalf("validPinnedImageRef(%q) passed", ref)
		}
	}
}

func TestCellImageRefResolution(t *testing.T) {
	c, fake := cellFixture(t, nil)
	// Artifact plan without a manifest digest denies before any docker call.
	req := cliReq("rg", cellExecution())
	req.Plan.Image = "hermes-cli-artifact/x"
	req.Command = "/bin/tool"
	if _, err := c.cellImageRef(context.Background(), req); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("image without digest: %v", err)
	}
	// image@digest unresolvable → falls back to the local name tag.
	req.Plan.Digest = "sha256:" + strings.Repeat("f", 64)
	fake.images["hermes-cli-artifact/x"] = "sha256:" + strings.Repeat("9", 64)
	ref, err := c.cellImageRef(context.Background(), req)
	if err != nil || ref != fake.images["hermes-cli-artifact/x"] {
		t.Fatalf("fallback resolve ref=%q err=%v", ref, err)
	}
	// Missing tools image fails closed.
	delete(fake.images, "hermes-hub-cli-tools:test")
	req = cliReq("rg", cellExecution())
	if _, err := c.cellImageRef(context.Background(), req); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("missing tools image: %v", err)
	}
}

func TestCellHelpers(t *testing.T) {
	if minDuration(2*time.Second, time.Second) != time.Second || minDuration(time.Second, 2*time.Second) != time.Second {
		t.Fatal("minDuration")
	}
	c, _ := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	if got := c.cellUID(req, true); got != "10001:10001" {
		t.Fatalf("default uid %q", got)
	}
	c.config.CLI.PerPrincipalUID = true
	if got := c.cellUID(req, false); got != "10001:10001" {
		t.Fatalf("ro workspace uid %q", got)
	}
	if got := c.cellUID(req, true); got == "10001:10001" || !strings.HasSuffix(got, ":"+strings.Split(got, ":")[0]) {
		t.Fatalf("per-principal uid %q", got)
	}
	if cellWarmKey(api.CLILifecycleTask, "alice", "b", "", "", nil) != "" {
		t.Fatal("task key without job")
	}
	if cellWarmKey(api.CLILifecycleTask, "alice", "", "job-1", "", nil) == "" {
		t.Fatal("task key missing")
	}
	if cellWarmKey(api.CLILifecycleToolbox, "alice", "", "", "img", []string{"b@x", "a@y"}) != cellWarmKey(api.CLILifecycleToolbox, "alice", "", "", "img", []string{"a@y", "b@x"}) {
		t.Fatal("toolbox key is order-sensitive")
	}
	if cellWarmKey(api.CLILifecycleEphemeral, "alice", "b", "", "", nil) != "" {
		t.Fatal("ephemeral has no warm key")
	}
	if (&genericController{}).cells() == nil {
		t.Fatal("cells() did not initialize the registry")
	}
}

func TestValidateExecRequestMatrix(t *testing.T) {
	c, _ := cellFixture(t, nil)
	base := func() api.CliExecRequest { return cliReq("rg", cellExecution()) }
	if err := c.validateExecRequest(base()); err != nil {
		t.Fatalf("base request denied: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*api.CliExecRequest)
		want   error
	}{
		{"no-config", func(r *api.CliExecRequest) {}, api.ErrIsolation},
		{"bad-workload", func(r *api.CliExecRequest) { r.Plan.WorkloadID = "!" }, api.ErrInvalid},
		{"bad-definition", func(r *api.CliExecRequest) { r.Plan.DefinitionID = "!" }, api.ErrInvalid},
		{"bad-version", func(r *api.CliExecRequest) { r.Plan.DefinitionVersion = "x" }, api.ErrInvalid},
		{"bad-principal", func(r *api.CliExecRequest) { r.Principal = "!" }, api.ErrInvalid},
		{"bad-context", func(r *api.CliExecRequest) { r.ContextID = "" }, api.ErrInvalid},
		{"bad-binding", func(r *api.CliExecRequest) { r.BindingID = "" }, api.ErrInvalid},
		{"bad-job", func(r *api.CliExecRequest) { r.JobID = "!" }, api.ErrInvalid},
		{"mcp-fields-mount", func(r *api.CliExecRequest) { r.Plan.Execution.Mounts = []api.Mount{{Source: "/x", Target: "/y"}} }, api.ErrInvalid},
		{"mcp-fields-sidecar", func(r *api.CliExecRequest) { r.Plan.SidecarImages = []string{"i"} }, api.ErrInvalid},
		{"mcp-fields-workspace", func(r *api.CliExecRequest) { r.Plan.WorkspacePath = "/x" }, api.ErrInvalid},
		{"over-ceiling", func(r *api.CliExecRequest) { r.Plan.Execution.TimeoutSeconds++ }, api.ErrUnauthorized},
		{"egress-denied", func(r *api.CliExecRequest) { r.Plan.Execution.Egress = []string{"evil.example.com"} }, api.ErrUnauthorized},
		{"slash-command", func(r *api.CliExecRequest) { r.Command = "bin/rg" }, api.ErrInvalid},
		{"bad-command", func(r *api.CliExecRequest) { r.Command = "not a cmd" }, api.ErrInvalid},
		{"empty-arg", func(r *api.CliExecRequest) { r.Args = []string{""} }, api.ErrInvalid},
		{"long-arg", func(r *api.CliExecRequest) { r.Args = []string{strings.Repeat("x", 300)} }, api.ErrInvalid},
		{"newline-arg", func(r *api.CliExecRequest) { r.Args = []string{"a\nb"} }, api.ErrInvalid},
		{"proxy-env", func(r *api.CliExecRequest) { r.Env = []string{"HTTP_PROXY=http://x"} }, api.ErrUnauthorized},
		{"path-env", func(r *api.CliExecRequest) { r.Env = []string{"PATH=/x"} }, api.ErrUnauthorized},
		{"ld-env", func(r *api.CliExecRequest) { r.Env = []string{"LD_PRELOAD=/x.so"} }, api.ErrUnauthorized},
		{"bad-env-key", func(r *api.CliExecRequest) { r.Env = []string{"NO-DASH=x"} }, api.ErrUnauthorized},
		{"env-no-value", func(r *api.CliExecRequest) { r.Env = []string{"KEYONLY"} }, api.ErrUnauthorized},
		{"env-newline", func(r *api.CliExecRequest) { r.Env = []string{"K=a\nb"} }, api.ErrUnauthorized},
		{"task-no-job", func(r *api.CliExecRequest) { r.Lifecycle = api.CLILifecycleTask }, api.ErrInvalid},
		{"task-workspace", func(r *api.CliExecRequest) {
			r.Lifecycle = api.CLILifecycleTask
			r.JobID = "job-1"
			r.Workspace = api.CliWorkspace{Scope: "binding", Path: "/x"}
		}, api.ErrInvalid},
		{"toolbox-no-id", func(r *api.CliExecRequest) { r.Lifecycle = api.CLILifecycleToolbox }, api.ErrInvalid},
		{"pool-not-stateless", func(r *api.CliExecRequest) { r.Lifecycle = api.CLILifecycleSharedPool }, api.ErrUnauthorized},
		{"pool-workspace", func(r *api.CliExecRequest) {
			r.Lifecycle = api.CLILifecycleSharedPool
			r.Stateless = true
			r.Workspace = api.CliWorkspace{Scope: "binding", Path: "/x"}
		}, api.ErrUnauthorized},
		{"unknown-lifecycle", func(r *api.CliExecRequest) { r.Lifecycle = "bogus" }, api.ErrInvalid},
		{"bad-cred-name", func(r *api.CliExecRequest) {
			r.Brokered = []api.CliBrokeredCred{{EnvName: "bad name", Host: "api.example.com", Value: "v"}}
		}, api.ErrInvalid},
		{"bad-cred-host", func(r *api.CliExecRequest) {
			r.Brokered = []api.CliBrokeredCred{{EnvName: "TOK", Host: "not a host!", Value: "v"}}
		}, api.ErrInvalid},
		{"empty-cred-value", func(r *api.CliExecRequest) {
			r.Brokered = []api.CliBrokeredCred{{EnvName: "TOK", Host: "api.example.com"}}
		}, api.ErrInvalid},
		{"unapproved", func(r *api.CliExecRequest) { r.Command = "rm" }, api.ErrUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := c
			req := base()
			if tc.name == "no-config" {
				target = &genericController{}
			}
			tc.mutate(&req)
			err := target.validateExecRequest(req)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	// Artifact plan shape: image+digest needs an absolute guest command.
	req := base()
	req.Plan.Image = "img"
	req.Plan.Digest = "sha256:" + strings.Repeat("d", 64)
	if err := c.validateExecRequest(req); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("artifact plan with bare command: %v", err)
	}
	// Too many env vars / brokered creds.
	req = base()
	req.Env = make([]string, 65)
	for i := range req.Env {
		req.Env[i] = fmt.Sprintf("K%d=v", i)
	}
	if err := c.validateExecRequest(req); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("env flood: %v", err)
	}
	req = base()
	req.Brokered = make([]api.CliBrokeredCred, 9)
	for i := range req.Brokered {
		req.Brokered[i] = api.CliBrokeredCred{EnvName: fmt.Sprintf("K%d", i), Host: "api.example.com", Value: "v"}
	}
	if err := c.validateExecRequest(req); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("brokered flood: %v", err)
	}
}

func TestValidateCLIConfigMatrix(t *testing.T) {
	base := func() *api.CLIControllerConfig {
		root := t.TempDir()
		cellinit := filepath.Join(root, "cellinit")
		if err := os.WriteFile(cellinit, []byte("x"), 0700); err != nil {
			t.Fatal(err)
		}
		return &api.CLIControllerConfig{
			ToolsImage:    "hermes-hub-cli-tools:test",
			CellInit:      cellinit,
			AllowedEgress: []string{"github.com", "127.0.0.1"},
			Ceiling:       cellExecution(),
			UserCommands:  []string{"rg"},
		}
	}
	if err := api.ValidateCLIConfig(base()); err != nil {
		t.Fatalf("base config denied: %v", err)
	}
	if err := api.ValidateCLIConfig(nil); err != nil {
		t.Fatal("nil config must pass")
	}
	cases := []struct {
		name   string
		mutate func(*api.CLIControllerConfig)
	}{
		{"bad-tools-image", func(c *api.CLIControllerConfig) { c.ToolsImage = "bad ref!!" }},
		{"no-cellinit", func(c *api.CLIControllerConfig) { c.CellInit = "" }},
		{"relative-cellinit", func(c *api.CLIControllerConfig) { c.CellInit = "rel/path" }},
		{"bad-proxy-image", func(c *api.CLIControllerConfig) { c.ProxyImage = "bad ref!!" }},
		{"unknown-runtime", func(c *api.CLIControllerConfig) { c.Runtime = "bogus" }},
		{"relative-spaces", func(c *api.CLIControllerConfig) { c.SpacesRoot = "rel" }},
		{"no-egress", func(c *api.CLIControllerConfig) { c.AllowedEgress = nil }},
		{"bad-egress-host", func(c *api.CLIControllerConfig) { c.AllowedEgress = []string{"not a host!"} }},
		{"zero-ceiling", func(c *api.CLIControllerConfig) { c.Ceiling = api.ExecutionPolicy{} }},
		{"no-approvals", func(c *api.CLIControllerConfig) { c.UserCommands = nil }},
		{"bad-approval-id", func(c *api.CLIControllerConfig) {
			c.Approved = []api.CLIApproval{{DefinitionID: "!", DefinitionVersion: "1.0.0", Command: "git", Execution: cellExecution()}}
		}},
		{"bad-approval-version", func(c *api.CLIControllerConfig) {
			c.Approved = []api.CLIApproval{{DefinitionID: "d", DefinitionVersion: "x!", Command: "git", Execution: cellExecution()}}
		}},
		{"approval-bad-command", func(c *api.CLIControllerConfig) {
			c.Approved = []api.CLIApproval{{DefinitionID: "d", DefinitionVersion: "1.0.0", Command: "a/b", Execution: cellExecution()}}
		}},
		{"artifact-approval-no-digest", func(c *api.CLIControllerConfig) {
			c.Approved = []api.CLIApproval{{DefinitionID: "d", DefinitionVersion: "1.0.0", Command: "/bin/t", Image: "img", Execution: cellExecution()}}
		}},
		{"artifact-approval-rel-cmd", func(c *api.CLIControllerConfig) {
			c.Approved = []api.CLIApproval{{DefinitionID: "d", DefinitionVersion: "1.0.0", Command: "tool", Image: "img", Digest: "sha256:" + strings.Repeat("d", 64), Execution: cellExecution()}}
		}},
		{"approval-over-ceiling", func(c *api.CLIControllerConfig) {
			over := cellExecution()
			over.TimeoutSeconds = 3600
			c.Approved = []api.CLIApproval{{DefinitionID: "d", DefinitionVersion: "1.0.0", Command: "git", Execution: over}}
		}},
		{"user-command-slash", func(c *api.CLIControllerConfig) { c.UserCommands = []string{"a/b"} }},
		{"user-command-shape", func(c *api.CLIControllerConfig) { c.UserCommands = []string{"bad cmd!"} }},
		{"toolbox-bad-name", func(c *api.CLIControllerConfig) {
			c.Toolboxes = map[string][]string{"!": {"img@" + "sha256:" + strings.Repeat("d", 64)}}
		}},
		{"toolbox-empty", func(c *api.CLIControllerConfig) { c.Toolboxes = map[string][]string{"tb": {}} }},
		{"toolbox-unpinned", func(c *api.CLIControllerConfig) { c.Toolboxes = map[string][]string{"tb": {"img:tag"}} }},
		{"pool-negative", func(c *api.CLIControllerConfig) { c.PoolSize = -1 }},
		{"pool-huge", func(c *api.CLIControllerConfig) { c.PoolSize = 17 }},
		{"warm-age-negative", func(c *api.CLIControllerConfig) { c.WarmMaxAgeSeconds = -1 }},
		{"warm-reuse-huge", func(c *api.CLIControllerConfig) { c.WarmMaxReuse = 1<<20 + 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			tc.mutate(cfg)
			if err := api.ValidateCLIConfig(cfg); err == nil {
				t.Fatalf("config %s passed", tc.name)
			}
		})
	}
	// Runtime names and egress wildcard are legal.
	cfg := base()
	cfg.Runtime = "runsc"
	cfg.AllowedEgress = []string{"*"}
	if err := api.ValidateCLIConfig(cfg); err != nil {
		t.Fatalf("runsc+wildcard denied: %v", err)
	}
}

func TestCellInitSeed(t *testing.T) {
	root := t.TempDir()
	cfg := func(cellinit string) *genericController {
		return &genericController{config: GenericControllerConfig{StateRoot: root, CLI: &api.CLIControllerConfig{CellInit: cellinit}}}
	}
	// Missing source file fails closed.
	if err := cfg(filepath.Join(root, "missing")).cellInitSeed(); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("missing cellinit: %v", err)
	}
	// A directory is not a binary.
	dir := filepath.Join(root, "dircell")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := cfg(dir).cellInitSeed(); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("directory cellinit: %v", err)
	}
	// A real file seeds into the state root once; the second call is a no-op.
	src := filepath.Join(root, "cellinit")
	if err := os.WriteFile(src, []byte("static-binary"), 0700); err != nil {
		t.Fatal(err)
	}
	c := cfg(src)
	if err := c.cellInitSeed(); err != nil {
		t.Fatal(err)
	}
	seeded := filepath.Join(root, "cli-cellinit")
	data, err := os.ReadFile(seeded)
	if err != nil || string(data) != "static-binary" {
		t.Fatalf("seeded content=%q err=%v", data, err)
	}
	if err := c.cellInitSeed(); err != nil {
		t.Fatal(err)
	}
}

func TestCellDriftedBranches(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	var cell *cliCell
	for _, member := range c.cells().cells {
		cell = member
	}
	c.mu.Unlock()
	if cell == nil {
		t.Fatal("no warm cell")
	}
	// Daemon lost the container → inspect error is drift.
	fake.mu.Lock()
	delete(fake.containers, cell.name)
	fake.mu.Unlock()
	if problem := c.cellDrifted(context.Background(), cell); !strings.HasPrefix(problem, "inspect:") {
		t.Fatalf("missing cell drift=%q", problem)
	}
	// Recreate, then fail the /tmp canary clean verb.
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	for name := range fake.containers {
		if strings.HasPrefix(name, "cli-") {
			cell = c.cells().cells[cellWarmKey(req.Lifecycle, req.Principal, req.BindingID, "", "sha256:"+strings.Repeat("a", 64), nil)]
			_ = name
		}
	}
	fake.mu.Unlock()
	fake.initExec = func(argv []string) (int, error) {
		if len(argv) > 1 && argv[1] == "clean" {
			return 1, nil
		}
		return 0, nil
	}
	if problem := c.cellDrifted(context.Background(), cell); problem != "canary-tmp" {
		t.Fatalf("dirty /tmp drift=%q", problem)
	}
	fake.initExec = func(argv []string) (int, error) {
		if len(argv) > 1 && argv[1] == "procs" {
			return 1, nil
		}
		return 0, nil
	}
	if problem := c.cellDrifted(context.Background(), cell); problem != "stray-procs" {
		t.Fatalf("stray procs drift=%q", problem)
	}
}

func TestCellEgressFailures(t *testing.T) {
	// No proxy image configured + real egress → fail closed before sidecars.
	c, _ := cellFixture(t, func(cfg *api.CLIControllerConfig) { cfg.ProxyImage = "" })
	req := cliReq("rg", cellExecution())
	req.Plan.Execution.Egress = []string{"github.com"}
	if _, err := c.execCLI(context.Background(), req); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("egress without proxy image: %v", err)
	}

	cases := []struct {
		name string
		fail func(args []string) bool
	}{
		{"network-create", func(args []string) bool { return args[0] == "network" && args[1] == "create" }},
		{"volume-create", func(args []string) bool { return args[0] == "volume" }},
		{"squid-create", func(args []string) bool { return args[0] == "create" && strings.HasSuffix(args[2], "-proxy") }},
		{"squid-cp", func(args []string) bool { return args[0] == "cp" }},
		{"squid-connect", func(args []string) bool { return args[0] == "network" && args[1] == "connect" }},
		{"squid-start", func(args []string) bool { return args[0] == "start" && strings.HasSuffix(args[1], "-proxy") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, fake := cellFixture(t, nil)
			fake.failWhen = func(args []string) error {
				if tc.fail(args) {
					return fmt.Errorf("daemon blew up on %v", args)
				}
				return nil
			}
			req := cliReq("rg", cellExecution())
			req.Plan.Execution.Egress = []string{"github.com"}
			if _, err := c.execCLI(context.Background(), req); err == nil {
				t.Fatal("egress failure succeeded")
			}
			// Every partial sidecar object is cleaned up.
			if len(fake.containers) != 0 {
				t.Fatalf("sidecar survived egress failure: %v", fake.containers)
			}
		})
	}
	// Brokered creds alone (loopback egress) still build the cred sidecar.
	c, fake := cellFixture(t, nil)
	req = cliReq("rg", cellExecution())
	req.Brokered = []api.CliBrokeredCred{{EnvName: "TOKEN", Host: "api.example.com", Value: "s3cret", Prefix: "Bearer "}}
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatalf("brokered exec denied: %v", err)
	}
	createdCred := false
	for _, call := range fake.createCalls() {
		for i, arg := range call {
			if arg == "--name" && strings.HasSuffix(call[i+1], "-cred") {
				createdCred = true
			}
		}
	}
	if !createdCred {
		t.Fatal("cred proxy was not created for a brokered request")
	}
	// Cred-proxy create failure tears the whole cell down.
	c, fake = cellFixture(t, nil)
	fake.failWhen = func(args []string) error {
		if args[0] == "create" && len(args) > 2 && strings.HasSuffix(args[2], "-cred") {
			return fmt.Errorf("cred create blew up")
		}
		return nil
	}
	if _, err := c.execCLI(context.Background(), req); err == nil {
		t.Fatal("cred-proxy failure succeeded")
	}
	if len(fake.containers) != 0 {
		t.Fatalf("objects survived cred-proxy failure: %v", fake.containers)
	}
}

func TestWarmCellCapacityAndBusySlot(t *testing.T) {
	c, _ := cellFixture(t, nil)
	c.config.MaxActive = 4
	req := func(binding string) api.CliExecRequest {
		r := cliReq("rg", cellExecution())
		r.Lifecycle = api.CLILifecycleBinding
		r.BindingID = binding
		return r
	}
	var cells []*cliCell
	for _, b := range []string{"bind-1", "bind-2", "bind-3", "bind-4"} {
		if _, err := c.execCLI(context.Background(), req(b)); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	for _, cell := range c.cells().cells {
		cells = append(cells, cell)
	}
	c.mu.Unlock()
	if len(cells) != 4 {
		t.Fatalf("warm cells=%d", len(cells))
	}
	// All slots held → the table cannot evict → deny.
	for _, cell := range cells {
		cell.execSlot <- struct{}{}
	}
	if _, err := c.execCLI(context.Background(), req("bind-5")); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("full warm table: %v", err)
	}
	for _, cell := range cells {
		<-cell.execSlot
	}
	// A slot frees → the oldest cell is evicted to make room.
	if _, err := c.execCLI(context.Background(), req("bind-5")); err != nil {
		t.Fatalf("capacity eviction denied: %v", err)
	}
	c.mu.Lock()
	if len(c.cells().cells) != 4 {
		t.Fatalf("post-eviction warm cells=%d", len(c.cells().cells))
	}
	c.mu.Unlock()
	// Busy slot with a cancelled caller returns the context error — reuse a
	// cell that survived the eviction (the evicted one is map-order chosen).
	c.mu.Lock()
	var cell *cliCell
	var survivorBinding string
	for key, member := range c.cells().cells {
		cell = member
		survivorBinding = strings.TrimPrefix(key, "binding|alice|")
		survivorBinding = survivorBinding[:strings.LastIndex(survivorBinding, "|")]
		break
	}
	c.mu.Unlock()
	if cell == nil {
		t.Fatal("surviving warm cell missing")
	}
	survivor := req(survivorBinding)
	cell.execSlot <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.execCLI(ctx, survivor); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("busy warm slot with cancelled ctx: %v", err)
	}
	<-cell.execSlot
}

func TestCleanupCellsMaxAgeAndPool(t *testing.T) {
	c, _ := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	poolReq := cliReq("rg", cellExecution())
	poolReq.Lifecycle = api.CLILifecycleSharedPool
	poolReq.Stateless = true
	if _, err := c.execCLI(context.Background(), poolReq); err != nil {
		t.Fatal(err)
	}
	// Age the warm cell past the 24h default and the pool cell past idle.
	c.mu.Lock()
	for _, cell := range c.cells().cells {
		cell.created = time.Now().Add(-25 * time.Hour)
	}
	for _, list := range c.cells().pool {
		for _, cell := range list {
			cell.lastUse = time.Now().Add(-time.Hour)
		}
	}
	c.mu.Unlock()
	c.cleanupCells(context.Background(), time.Now())
	c.mu.Lock()
	warm, pooled := len(c.cells().cells), 0
	for _, list := range c.cells().pool {
		pooled += len(list)
	}
	c.mu.Unlock()
	if warm != 0 || pooled != 0 {
		t.Fatalf("cleanup left warm=%d pooled=%d", warm, pooled)
	}
	// Task cells get the shorter idle bound even with a long controller TTL.
	c.config.IdleTTLSeconds = 86400
	task := cliReq("rg", cellExecution())
	task.Lifecycle = api.CLILifecycleTask
	task.JobID = "job-1"
	if _, err := c.execCLI(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	for _, cell := range c.cells().cells {
		cell.lastUse = time.Now().Add(-10 * time.Minute)
		cell.created = time.Now().Add(-10 * time.Minute)
	}
	c.mu.Unlock()
	c.cleanupCells(context.Background(), time.Now())
	c.mu.Lock()
	remaining := len(c.cells().cells)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("task cell survived 10m idle: %d", remaining)
	}
}

func TestPoolWaitCancelled(t *testing.T) {
	c, fake := cellFixture(t, func(cfg *api.CLIControllerConfig) { cfg.PoolSize = 1 })
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleSharedPool
	req.Stateless = true
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Park the pooled cell's exec slot so the next claim finds it busy.
	c.mu.Lock()
	image := ""
	for key := range c.cells().pool {
		image = key
	}
	pooled := c.cells().pool[image][0]
	c.mu.Unlock()
	pooled.execSlot <- struct{}{}
	defer func() { <-pooled.execSlot }()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.execCLI(ctx, req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy pool wait: %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("pool wait ignored ctx")
	}
	if fake.exec != nil {
		t.Fatal("unreachable")
	}
}

// The containerd image store reports --tmpfs in HostConfig.Tmpfs, the classic
// store in .Mounts. The profile accepts either — and still denies stray
// scratch dirs and extra mounts.
func TestCellProfileProblemTmpfsRepresentations(t *testing.T) {
	c, _ := cellFixture(t, nil)
	seccomp := c.config.SeccompProfile
	stateRoot := c.config.StateRoot
	cell := &cliCell{name: "cli-x", principal: "alice", lifecycle: api.CLILifecycleEphemeral, image: "img-ref"}
	bindMount := genericMount{Type: "bind", Destination: cellInitGuest,
		Source: api.DockerBindSource(filepath.Join(stateRoot, "cli-cellinit"))}
	base := func() *genericContainer {
		container := &genericContainer{}
		container.State.Running = true
		container.HostConfig.ReadonlyRootfs = true
		container.HostConfig.CapDrop = []string{"ALL"}
		container.HostConfig.SecurityOpt = []string{"no-new-privileges=true", "seccomp=" + seccomp}
		container.HostConfig.NetworkMode = "none"
		container.Config.Image = cell.image
		container.Config.User = "10001:10001"
		container.Config.Labels = map[string]string{
			"hermes-hub.cell": "true", "hermes-hub.cell.principal": "alice", "hermes-hub.cell.lifecycle": api.CLILifecycleEphemeral,
		}
		return container
	}

	// containerd representation: tmpfs only in HostConfig.Tmpfs.
	container := base()
	container.Mounts = []genericMount{bindMount}
	container.HostConfig.Tmpfs = map[string]string{"/tmp": "", cellWorkGuest: ""}
	if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != "" {
		t.Fatalf("containerd profile denied: %s", got)
	}
	// classic representation: tmpfs entries inside .Mounts.
	container = base()
	container.Mounts = []genericMount{bindMount,
		{Type: "tmpfs", Destination: "/tmp", RW: true},
		{Type: "tmpfs", Destination: cellWorkGuest, RW: true}}
	if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != "" {
		t.Fatalf("classic profile denied: %s", got)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*genericContainer)
		want   string
	}{
		{"stray-tmpfs", func(g *genericContainer) { g.HostConfig.Tmpfs["/evil"] = "" }, "tmpfs:/evil"},
		{"missing-tmp", func(g *genericContainer) { delete(g.HostConfig.Tmpfs, "/tmp") }, "tmp-mount"},
		{"missing-work", func(g *genericContainer) { delete(g.HostConfig.Tmpfs, cellWorkGuest) }, "work-mount"},
		{"missing-cellinit", func(g *genericContainer) { g.Mounts = nil }, "mount-missing"},
		{"foreign-mount", func(g *genericContainer) {
			g.Mounts = append(g.Mounts, genericMount{Type: "bind", Destination: "/state", Source: "/state"})
		}, "mount:/state"},
		{"rw-cellinit", func(g *genericContainer) { g.Mounts[0].RW = true }, "cellinit-mount"},
		{"work-bind-scratch", func(g *genericContainer) {
			g.Mounts = append(g.Mounts, genericMount{Type: "bind", Destination: cellWorkGuest, Source: "/x"})
		}, "work-mount"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := base()
			container.Mounts = []genericMount{bindMount}
			container.HostConfig.Tmpfs = map[string]string{"/tmp": "", cellWorkGuest: ""}
			tc.mutate(container)
			if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	// A workspace-binding cell: /work is a bind, no work tmpfs wanted.
	container = base()
	cell.workHost = filepath.Join(stateRoot, "ws")
	cell.workRW = true
	container.Mounts = []genericMount{bindMount,
		{Type: "bind", Destination: cellWorkGuest, Source: cell.workHost, RW: true}}
	container.HostConfig.Tmpfs = map[string]string{"/tmp": ""}
	if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != "" {
		t.Fatalf("workspace-bind profile denied: %s", got)
	}
	container.HostConfig.Tmpfs[cellWorkGuest] = ""
	if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != "tmpfs:"+cellWorkGuest {
		t.Fatalf("stray work tmpfs on bind cell: %s", got)
	}
	delete(container.HostConfig.Tmpfs, cellWorkGuest)
	container.Mounts[1].RW = false
	if got := cellProfileProblem(*container, cell, seccomp, stateRoot, true); got != "work-mount" {
		t.Fatalf("rw drift on workspace bind: %s", got)
	}
}

func TestRemoveCellFailureSurvivesLog(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	cell := &cliCell{name: "cli-stuck", principal: "alice", lifecycle: api.CLILifecycleEphemeral}
	fake.containers[cell.name] = &genericContainer{}
	fake.failWhen = func(args []string) error {
		if args[0] == "rm" {
			return fmt.Errorf("rm denied")
		}
		return nil
	}
	// rm fails, the container still inspects → the "survived" audit branch runs.
	c.removeCell(context.Background(), cell, "test")
	if _, ok := fake.containers[cell.name]; !ok {
		t.Fatal("container vanished despite failed rm")
	}
}

func TestCellRunScopeDeny(t *testing.T) {
	c, _ := cellFixture(t, nil)
	// A scope that answers every inspect as a foreign project's container.
	c.scope = api.NewDockerScope("other-stack", "", func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		return []byte(`{"com.docker.compose.project":"foreign"}|{}`), nil
	})
	if _, err := c.cellRun(context.Background(), io.Discard, "exec", "foreign-container", "x"); !errors.Is(err, api.ErrIsolation) {
		t.Fatalf("foreign exec under scope: %v", err)
	}
}

func TestBoundedOutputSecondWrite(t *testing.T) {
	out := &api.BoundedOutput{Limit: 4, Exceeded: make(chan struct{})}
	if _, err := out.Write([]byte("abcdef")); !errors.Is(err, api.ErrOutputLimit) {
		t.Fatalf("first overflow: %v", err)
	}
	<-out.Exceeded
	if n, err := out.Write([]byte("x")); n != 0 || !errors.Is(err, api.ErrOutputLimit) {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
	if !out.Overflowed() || out.String() != "abcd" {
		t.Fatalf("output=%q", out.String())
	}
}

func TestSubmitPlanFailures(t *testing.T) {
	plan := api.ControllerPlan{WorkloadID: "w-1"}
	effective := api.EffectiveBinding{}
	server := func(handler http.HandlerFunc) (*httptest.Server, *http.Client) {
		srv := httptest.NewServer(handler)
		return srv, srv.Client()
	}
	t.Run("status", func(t *testing.T) {
		srv, client := server(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusForbidden)
		})
		defer srv.Close()
		if _, err := api.SubmitPlan(context.Background(), client, srv.URL, "", plan, effective); err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("403 submit: %v", err)
		}
	})
	t.Run("no-receipt", func(t *testing.T) {
		srv, client := server(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
		defer srv.Close()
		if _, err := api.SubmitPlan(context.Background(), client, srv.URL, "", plan, effective); err == nil || !strings.Contains(err.Error(), "receipt") {
			t.Fatalf("204 submit: %v", err)
		}
	})
	t.Run("oversize", func(t *testing.T) {
		srv, client := server(func(w http.ResponseWriter, r *http.Request) {
			w.Write(make([]byte, 70*1024))
		})
		defer srv.Close()
		if _, err := api.SubmitPlan(context.Background(), client, srv.URL, "", plan, effective); err == nil || !strings.Contains(err.Error(), "too large") {
			t.Fatalf("oversize submit: %v", err)
		}
	})
	t.Run("bad-json", func(t *testing.T) {
		srv, client := server(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("{nope")) })
		defer srv.Close()
		if _, err := api.SubmitPlan(context.Background(), client, srv.URL, "", plan, effective); err == nil || !strings.Contains(err.Error(), "receipt") {
			t.Fatalf("bad json submit: %v", err)
		}
	})
	t.Run("invalid-receipt", func(t *testing.T) {
		srv, client := server(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"workload_id":"other"}`)) })
		defer srv.Close()
		if _, err := api.SubmitPlan(context.Background(), client, srv.URL, "", plan, effective); err == nil {
			t.Fatal("mismatched receipt accepted")
		}
	})
}

func TestCLIReleaseFromEnv(t *testing.T) {
	// Unset endpoint → nil releaser.
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	release, err := api.CLIReleaseFromEnv()
	if err != nil || release != nil {
		t.Fatalf("unset endpoint gave releaser err=%v", err)
	}
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-release" || r.Method != http.MethodPost {
			t.Errorf("release hit %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("release without bearer token")
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", srv.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "tok")
	release, err = api.CLIReleaseFromEnv()
	if err != nil || release == nil {
		t.Fatalf("releaser missing err=%v", err)
	}
	if err := release(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "binding-release"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gotBody), "alice") || !strings.Contains(string(gotBody), "bind-1") {
		t.Fatalf("release body=%s", gotBody)
	}
	// A non-2xx answer is an error.
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	if err := release(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "binding-release"}); err == nil {
		t.Fatal("500 release accepted")
	}
}

func TestCLIRunnerFromEnv(t *testing.T) {
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	runner, err := api.CLIRunnerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if runner.Exec != nil {
		t.Fatal("runner gained an exec channel without an endpoint")
	}
	if runner.Root == "" {
		t.Fatal("runner has no root")
	}
}

func TestReleaseCellsSelector(t *testing.T) {
	c, _ := cellFixture(t, nil)
	// Invalid principal is a no-op.
	c.releaseCells(context.Background(), api.CliReleaseRequest{PrincipalID: "!"})
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleBinding
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	// Selector that does not match the cell's binding keeps it.
	c.releaseCells(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "other"})
	c.mu.Lock()
	if len(c.cells().cells) != 1 {
		t.Fatalf("unmatched release killed cells=%d", len(c.cells().cells))
	}
	c.mu.Unlock()
	// A matching selector kills.
	c.releaseCells(context.Background(), api.CliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "revoke"})
	c.mu.Lock()
	remaining := len(c.cells().cells)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("release left %d cells", remaining)
	}
}

func TestCellMetricsSnapshotAndPoolFailure(t *testing.T) {
	c, fake := cellFixture(t, nil)
	req := cliReq("rg", cellExecution())
	req.Lifecycle = api.CLILifecycleBinding
	req.BindingID = "bind-m"
	if _, err := c.execCLI(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	poolReq := cliReq("rg", cellExecution())
	poolReq.Lifecycle = api.CLILifecycleSharedPool
	poolReq.Stateless = true
	if _, err := c.execCLI(context.Background(), poolReq); err != nil {
		t.Fatal(err)
	}
	snap := c.cellMetricsSnapshot()
	for _, key := range []string{"creates", "reuses", "canary_fails", "rotations", "pool_hits", "pool_misses", "pool_claims", "pool_exhaustions", "claim_wait_nanos", "warm_cells", "pool_idle", "pool_claimed"} {
		if _, ok := snap[key]; !ok {
			t.Fatalf("cli-metrics missing %q", key)
		}
	}
	if snap["creates"] != 2 || snap["warm_cells"] != 1 || snap["pool_idle"] != 1 {
		t.Fatalf("cli-metrics snapshot=%v", snap)
	}
	// A pooled first-exec failure drops the fresh cell instead of pooling it.
	fake.exec = func(argv []string, sink io.Writer) (int, error) {
		return 1, fmt.Errorf("tool exploded")
	}
	if _, err := c.execCLI(context.Background(), poolReq); err == nil {
		t.Fatal("pool exec failure swallowed")
	}
	c.mu.Lock()
	idle := 0
	for _, list := range c.cells().pool {
		idle += len(list)
	}
	c.mu.Unlock()
	if idle != 0 {
		t.Fatalf("failed pool exec kept a poisoned cell idle=%d", idle)
	}
	// With the pool empty the next failure exercises the fresh-cell path.
	if _, err := c.execCLI(context.Background(), poolReq); err == nil {
		t.Fatal("direct pool exec failure swallowed")
	}
}

func TestResolveToolboxFallback(t *testing.T) {
	c, fake := cellFixture(t, func(cfg *api.CLIControllerConfig) {
		cfg.Toolboxes = map[string][]string{"box": {
			"ghcr.io/example/pinned@sha256:" + strings.Repeat("8", 64),
			"ghcr.io/example/local@sha256:" + strings.Repeat("c", 64),
			"ghcr.io/example/missing@sha256:" + strings.Repeat("d", 64),
		}}
	})
	fake.images["ghcr.io/example/pinned@sha256:"+strings.Repeat("8", 64)] = "sha256:" + strings.Repeat("8", 64)
	fake.images["ghcr.io/example/local"] = "sha256:" + strings.Repeat("c", 64)
	// The third member resolves neither by pin nor by name → toolbox fails.
	if _, err := c.resolveToolbox(context.Background(), "box"); err == nil {
		t.Fatal("unresolvable member admitted")
	}
	delete(c.config.CLI.Toolboxes, "box")
	c.config.CLI.Toolboxes["box"] = []string{
		"ghcr.io/example/pinned@sha256:" + strings.Repeat("8", 64),
		"ghcr.io/example/local@sha256:" + strings.Repeat("c", 64),
	}
	members, err := c.resolveToolbox(context.Background(), "box")
	if err != nil {
		t.Fatalf("toolbox resolve: %v", err)
	}
	if len(members) != 2 || members[1] != "sha256:"+strings.Repeat("c", 64) {
		t.Fatalf("members=%v", members)
	}
	if _, err := c.resolveToolbox(context.Background(), "nope"); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("unknown toolbox: %v", err)
	}
}
