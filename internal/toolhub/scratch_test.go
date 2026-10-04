package toolhub

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func scratchTar(t *testing.T, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for name, body := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// Exports never land without an apply-time authority recheck, and each
// member goes through the create-only write channel under <path>/exports/.
func TestApplyExportsFenceAndWrites(t *testing.T) {
	var written []string
	apply := func(_ context.Context, _ EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
		path, _ := request.Arguments["path"].(string)
		written = append(written, path)
		return AgentExecResult{Result: map[string]any{"path": path, "revision": 1}}, nil
	}
	request := AgentExecRequest{Scopes: []CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}}
	effective := EffectiveBinding{}
	if _, _, err := applyExports(context.Background(), apply, nil, effective, request, "docs", scratchTar(t, map[string]string{"a.txt": "x"})); !errors.Is(err, ErrIsolation) {
		t.Fatalf("export without fence must fail closed: %v", err)
	}
	deny := errors.New("stale revision")
	if _, _, err := applyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return deny }, effective, request, "docs", scratchTar(t, map[string]string{"a.txt": "x"})); !errors.Is(err, deny) {
		t.Fatalf("fence denial must stop the export: %v", err)
	}
	applied, skipped, err := applyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return nil }, effective, request, "docs", scratchTar(t, map[string]string{"out.txt": "data", "sub/plan.md": "p"}))
	if err != nil || len(applied) != 2 || len(skipped) != 0 {
		t.Fatalf("export failed: %v applied=%v skipped=%v", err, applied, skipped)
	}
	got := map[string]bool{}
	for _, name := range written {
		got[name] = true
	}
	if !got["docs/exports/out.txt"] || !got["docs/exports/sub/plan.md"] || len(written) != 2 {
		t.Fatalf("export landed outside the admitted prefix: %v", written)
	}
	// Symlink and traversal members are dropped, never applied.
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	_ = writer.WriteHeader(&tar.Header{Name: "evil.txt", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = writer.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	_, _ = writer.Write([]byte("x"))
	_ = writer.WriteHeader(&tar.Header{Name: "ok.txt", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg})
	_, _ = writer.Write([]byte("y"))
	_ = writer.Close()
	written = nil
	applied, skipped, err = applyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return nil }, effective, request, "docs", base64.StdEncoding.EncodeToString(raw.Bytes()))
	if err != nil || len(applied) != 1 || len(skipped) != 2 {
		t.Fatalf("hostile members must be skipped: %v applied=%v skipped=%v", err, applied, skipped)
	}
	// A channel-level file_write error (revision conflict, existing file)
	// skips that member instead of failing the batch or overwriting.
	written = nil
	apply = func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{Error: "path already exists"}, nil
	}
	if applied, skipped, err = applyExports(context.Background(), apply, func(context.Context, EffectiveBinding) error { return nil }, effective, request, "docs", scratchTar(t, map[string]string{"dup.txt": "x"})); err != nil || len(applied) != 0 || len(skipped) != 1 {
		t.Fatalf("existing target must skip, not overwrite: %v %v %v", err, applied, skipped)
	}
}

// The export fence re-checks the admitted authority: a revoked binding or a
// bumped policy/profile revision stops the write.
func TestReverifyEffective(t *testing.T) {
	s := NewStore()
	definition := agentDefinition()
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: PerUser, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	rule := CapabilityRule{CapabilityID: "files", ImplementationDigest: DefinitionDigest(definition), Action: "read", Resource: "files", PathPrefix: "docs", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
	policy := CapabilityPolicy{Schema: SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "test", Status: ActiveStatus, Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	profile := CapabilityProfile{Schema: SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "test", Status: ActiveStatus,
		Selections: []CapabilitySelection{{CapabilityID: "files", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: DefinitionDigest(definition),
			ToolName: "file_read", Name: "workspace_read"}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	_, effective, err := s.managedSelectionLocked(auth, profile, policy, profile.Selections[0], map[string]any{"path": "docs/a.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReverifyEffective(effective); err != nil {
		t.Fatalf("fresh authority must verify: %v", err)
	}
	// Policy revision bump — authority the call was admitted under is stale.
	policy.Revision++
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := s.ReverifyEffective(effective); err == nil {
		t.Fatal("stale policy revision passed the export fence")
	}
	// Profile bump (tracking the new policy revision) also goes stale.
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if err := s.ReverifyEffective(effective); err == nil {
		t.Fatal("stale profile revision passed the export fence")
	}
	// A revoked binding fails even before revision comparisons. Bindings are
	// immutable through PutBinding; revocation lands via the revoke path —
	// simulate its store-level effect directly.
	binding.Status = RevokedStatus
	s.mu.Lock()
	s.bindings[binding.ToolBindingID] = binding
	s.mu.Unlock()
	if err := s.ReverifyEffective(effective); err == nil {
		t.Fatal("revoked binding passed the export fence")
	}
}

// TestScratchDockerHelper impersonates the docker CLI for the contract tests
// below. State lives in $FAKE_DOCKER_STATE: "removed" records rm -f targets,
// "neverdie"+"stuck" emulate an executor that survives rm -f, and "log"
// accumulates one invocation per line.
func TestScratchDockerHelper(t *testing.T) {
	if os.Getenv("FAKE_DOCKER") != "1" {
		return
	}
	state := os.Getenv("FAKE_DOCKER_STATE")
	args := os.Args
	sep := 0
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	args = args[sep+1:]
	log, _ := os.OpenFile(filepath.Join(state, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	fmt.Fprintln(log, strings.Join(args, " "))
	log.Close()
	// FAKE_DOCKER_FAIL names a subcommand that must exit nonzero with stderr.
	if fail := os.Getenv("FAKE_DOCKER_FAIL"); fail != "" && strings.HasPrefix(args[0], fail) {
		fmt.Fprintln(os.Stderr, "daemon gone")
		os.Exit(3)
	}
	switch args[0] {
	case "inspect":
		name := args[len(args)-1]
		if name == "runtime-1" {
			fmt.Println("img:latest")
			os.Exit(0)
		}
		// An executor that refuses to die: while "neverdie" exists, the FIRST
		// scratch name stays inspectable even after rm -f; later names die
		// normally so sibling bindings keep working.
		if _, err := os.Stat(filepath.Join(state, "neverdie")); err == nil && strings.HasPrefix(name, "hermes-scratch-") {
			stuckPath := filepath.Join(state, "stuck")
			stuck, _ := os.ReadFile(stuckPath)
			if len(stuck) == 0 {
				_ = os.WriteFile(stuckPath, []byte(name), 0o600)
				stuck = []byte(name)
			}
			if string(stuck) == name {
				fmt.Println("{}")
				os.Exit(0)
			}
		}
		removed, _ := os.ReadFile(filepath.Join(state, "removed"))
		if strings.Contains(string(removed), name) {
			os.Exit(1)
		}
		fmt.Println("{}")
	case "exec":
		if args[len(args)-1] == "exec-pack" {
			var buf bytes.Buffer
			w := tar.NewWriter(&buf)
			_ = w.WriteHeader(&tar.Header{Name: "in.txt", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
			_, _ = w.Write([]byte("inp"))
			_ = w.Close()
			os.Stdout.Write(buf.Bytes())
			os.Exit(0)
		}
		var request AgentExecRequest
		_ = json.NewDecoder(os.Stdin).Decode(&request)
		_ = json.NewEncoder(os.Stdout).Encode(AgentExecResult{Result: map[string]any{"path": request.Arguments["path"], "revision": 1}})
	case "run":
		var request ScratchExecRequest
		_ = json.NewDecoder(os.Stdin).Decode(&request)
		if request.Command == "junk" {
			fmt.Println("not-json")
			os.Exit(0)
		}
		result := ScratchExecResult{ExitCode: 0, Stdout: "ran:" + request.Command, Stopped: true}
		if request.Command == "export" {
			result.ExportTar = func() string {
				var buf bytes.Buffer
				w := tar.NewWriter(&buf)
				_ = w.WriteHeader(&tar.Header{Name: "out.txt", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
				_, _ = w.Write([]byte("out"))
				_ = w.Close()
				return base64.StdEncoding.EncodeToString(buf.Bytes())
			}()
		}
		_ = json.NewEncoder(os.Stdout).Encode(result)
	case "rm":
		f, _ := os.OpenFile(filepath.Join(state, "removed"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fmt.Fprintln(f, args[len(args)-1])
		f.Close()
	}
	// Exit directly: the test framework's PASS line must not leak into the
	// captured docker stdout.
	os.Exit(0)
}

func fakeScratch(t *testing.T) ([]string, string, ScratchExecFunc) {
	t.Helper()
	state := t.TempDir()
	t.Setenv("FAKE_DOCKER", "1")
	t.Setenv("FAKE_DOCKER_STATE", state)
	docker := []string{os.Args[0], "-test.run=TestScratchDockerHelper", "--"}
	apply := func(_ context.Context, _ EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{Result: map[string]any{"path": request.Arguments["path"], "revision": 1}}, nil
	}
	exec := DockerScratchExec(docker, func(EffectiveBinding) (string, error) { return "runtime-1", nil }, apply)
	return docker, state, exec
}

func scratchRequest(command string) AgentExecRequest {
	return AgentExecRequest{Arguments: map[string]any{"command": command, "path": "docs"}, Scopes: []CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}}
}

func dockerLog(t *testing.T, state string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(state, "log"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(body)), "\n")
}

// The disposable executor: inspect → pack → run → export-apply → rm →
// inspect-confirmed, in that order, with the hardened container spec.
func TestDockerScratchExecHappyPath(t *testing.T) {
	_, state, exec := fakeScratch(t)
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-happy"}}
	out, err := exec(context.Background(), effective, scratchRequest("export"), func(context.Context, EffectiveBinding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := out.Result.(map[string]any)
	if summary["stopped"] != true || summary["exit_code"] != 0 {
		t.Fatalf("result lost stop state: %+v", summary)
	}
	applied, _ := summary["applied"].([]string)
	if len(applied) != 1 || applied[0] != "docs/exports/out.txt" {
		t.Fatalf("export did not land under the admitted prefix: %+v", summary)
	}
	log := dockerLog(t, state)
	// The apply channel is a stub func here; production wires it to
	// `docker exec <runtime> hubctl tools-exec` (DockerAgentExec), covered by
	// the supervisor canary. The docker-side contract is
	// inspect → pack → run → rm → inspect-confirm.
	order := []string{"inspect", "exec", "run", "rm", "inspect"}
	if len(log) != len(order) {
		t.Fatalf("unexpected docker call sequence: %v", log)
	}
	for i, want := range order {
		if !strings.HasPrefix(log[i], want) {
			t.Fatalf("call %d: want %s, got %q (full: %v)", i, want, log[i], log)
		}
	}
	runSpec := log[2]
	for _, flag := range []string{"--network none", "--read-only", "--cap-drop ALL", "no-new-privileges:true", "--user 10001:10001", "--tmpfs /scratch", "--tmpfs /outputs", "--memory 256m", "--cpus 0.5", "--pids-limit 64", "img:latest hubctl exec-scratch"} {
		if !strings.Contains(runSpec, flag) {
			t.Fatalf("sandbox spec missing %q: %s", flag, runSpec)
		}
	}
	for _, banned := range []string{"--privileged", "--network host", "docker.sock", "-v /", "--pid=host"} {
		if strings.Contains(runSpec, banned) {
			t.Fatalf("sandbox spec contains banned %q: %s", banned, runSpec)
		}
	}
	if !strings.HasPrefix(log[1], "exec -i runtime-1 hubctl exec-pack") {
		t.Fatalf("inputs were not packed through the scoped channel: %s", log[1])
	}
}

// An executor that refuses to die never reports success: the binding is
// quarantined until docker inspect confirms the workload is gone.
func TestDockerScratchExecUnconfirmedStop(t *testing.T) {
	_, state, exec := fakeScratch(t)
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-stuck"}}
	ok := func(context.Context, EffectiveBinding) error { return nil }
	if err := os.WriteFile(filepath.Join(state, "neverdie"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := exec(context.Background(), effective, scratchRequest("run"), ok); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unconfirmed stop must not report success: %v", err)
	}
	// The quarantined binding denies every replacement lease while the
	// executor may still be live.
	if _, err := exec(context.Background(), effective, scratchRequest("run"), ok); !errors.Is(err, ErrIsolation) {
		t.Fatalf("quarantined binding issued a replacement lease: %v", err)
	}
	other := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-clean"}}
	if _, err := exec(context.Background(), other, scratchRequest("run"), ok); err != nil {
		t.Fatalf("quarantine leaked to another binding: %v", err)
	}
	// Once inspect confirms the executor is gone the binding unblocks.
	if err := os.Remove(filepath.Join(state, "neverdie")); err != nil {
		t.Fatal(err)
	}
	if _, err := exec(context.Background(), effective, scratchRequest("run"), ok); err != nil {
		t.Fatalf("confirmed stop did not lift the quarantine: %v", err)
	}
}

// A fence denial at apply time keeps the run result but blocks the write:
// exports are reported, never applied.
func TestDockerScratchExecExportFence(t *testing.T) {
	_, state, exec := fakeScratch(t)
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-fence"}}
	out, err := exec(context.Background(), effective, scratchRequest("export"), func(context.Context, EffectiveBinding) error {
		return errors.New("capability policy changed")
	})
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := out.Result.(map[string]any)
	if summary["export_error"] == nil || len(summary["applied"].([]string)) != 0 {
		t.Fatalf("stale authority exported artifacts: %+v", summary)
	}
	log := dockerLog(t, state)
	for _, line := range log {
		if strings.Contains(line, "tools-exec") {
			t.Fatalf("export wrote through a denied fence: %v", log)
		}
	}
}

// Failure plumbing: every channel error surfaces, never a silent success.
func TestDockerScratchExecFailures(t *testing.T) {
	ok := func(context.Context, EffectiveBinding) error { return nil }
	effective := EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-fail"}}
	exec := func(state string) ScratchExecFunc {
		t.Helper()
		docker := []string{os.Args[0], "-test.run=TestScratchDockerHelper", "--"}
		return DockerScratchExec(docker, func(EffectiveBinding) (string, error) { return "runtime-1", nil },
			func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
				return AgentExecResult{}, nil
			})
	}
	// Bad arguments are rejected before any docker call.
	if _, err := exec(t.TempDir())(context.Background(), effective, AgentExecRequest{Arguments: map[string]any{"command": "", "path": "docs"}}, ok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty command reached the executor: %v", err)
	}
	if _, err := exec(t.TempDir())(context.Background(), effective, AgentExecRequest{Arguments: map[string]any{"command": "x", "path": "../up"}}, ok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("traversal path reached the executor: %v", err)
	}
	state := t.TempDir()
	t.Setenv("FAKE_DOCKER", "1")
	t.Setenv("FAKE_DOCKER_STATE", state)
	// Image lookup failure.
	t.Setenv("FAKE_DOCKER_FAIL", "inspect")
	if _, err := exec(state)(context.Background(), effective, scratchRequest("run"), ok); err == nil || !strings.Contains(err.Error(), "image lookup") {
		t.Fatalf("image lookup failure must surface: %v", err)
	}
	// Pack failure.
	t.Setenv("FAKE_DOCKER_FAIL", "exec")
	if _, err := exec(state)(context.Background(), effective, scratchRequest("run"), ok); err == nil || !strings.Contains(err.Error(), "materialization") {
		t.Fatalf("pack failure must surface: %v", err)
	}
	// Run failure: the container is still rm -f'd and confirmed gone.
	t.Setenv("FAKE_DOCKER_FAIL", "run")
	if _, err := exec(state)(context.Background(), EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-runfail"}}, scratchRequest("run"), ok); err == nil || !strings.Contains(err.Error(), "execution failed") {
		t.Fatalf("run failure must surface: %v", err)
	}
	t.Setenv("FAKE_DOCKER_FAIL", "")
	// An unparseable sandbox result is an error, not a silent empty result.
	if _, err := exec(state)(context.Background(), EffectiveBinding{Binding: ToolBinding{ToolBindingID: "bind-junk"}}, scratchRequest("junk"), ok); err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("garbage result must surface: %v", err)
	}
}

// The per-call executor channel: one request in, one bounded result out.
func TestDockerAgentExecChannel(t *testing.T) {
	state := t.TempDir()
	t.Setenv("FAKE_DOCKER", "1")
	t.Setenv("FAKE_DOCKER_STATE", state)
	docker := []string{os.Args[0], "-test.run=TestScratchDockerHelper", "--"}
	exec := DockerAgentExec(docker, func(EffectiveBinding) (string, error) { return "runtime-1", nil })
	out, err := exec(context.Background(), EffectiveBinding{}, AgentExecRequest{Tool: "file_read", Arguments: map[string]any{"path": "docs/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := out.Result.(map[string]any)
	if result["path"] != "docs/a.txt" {
		t.Fatalf("result did not round-trip: %+v", out)
	}
	log := dockerLog(t, state)
	if len(log) != 1 || !strings.HasPrefix(log[0], "exec -i runtime-1 hubctl tools-exec") {
		t.Fatalf("executor channel wrong: %v", log)
	}
	t.Setenv("FAKE_DOCKER_FAIL", "exec")
	if _, err := exec(context.Background(), EffectiveBinding{}, AgentExecRequest{Tool: "x"}); err == nil || !strings.Contains(err.Error(), "executor failed") {
		t.Fatalf("channel failure must surface: %v", err)
	}
}

func TestFixedAgentContainerAndGroupValidation(t *testing.T) {
	if _, err := FixedAgentContainer("")(EffectiveBinding{}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("empty container must fail closed: %v", err)
	}
	if name, err := FixedAgentContainer("runtime-9")(EffectiveBinding{}); err != nil || name != "runtime-9" {
		t.Fatalf("fixed container resolve: %s %v", name, err)
	}
	valid := CapabilityGroup{GroupID: "readers", Revision: 1, Members: []CapabilityRule{{CapabilityID: "files", ImplementationDigest: DefinitionDigest(agentDefinition()), Action: "read", Resource: "files", PathPrefix: "docs", Limits: CapabilityLimits{OutputBytes: 1024, TimeoutSeconds: 10}}}}
	if err := ValidateCapabilityGroup(valid); err != nil {
		t.Fatalf("valid group rejected: %v", err)
	}
	invalid := valid
	invalid.GroupID = ""
	if err := ValidateCapabilityGroup(invalid); err == nil {
		t.Fatal("invalid group accepted")
	}
}

// A sandboxed tool may only live on the agent-tools transport.
func TestSandboxedTransportGate(t *testing.T) {
	definition := agentDefinition()
	definition.Transport = RemoteMCP
	definition.Tools[0].Sandboxed = true
	definition.Source = DefinitionSource{Command: "x"}
	if err := definition.Validate(); err == nil {
		t.Fatal("sandboxed tool survived on a remote transport")
	}
	definition = agentDefinition()
	definition.Tools[0].Sandboxed = true
	if err := definition.Validate(); err != nil {
		t.Fatalf("sandboxed agent-tools spec must validate: %v", err)
	}
	// Sandboxed dispatches to Scratch, never Exec; missing scratch fails closed.
	e := EffectiveBinding{Definition: definition}
	execRan := false
	backend := AgentExecBackend{Exec: func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		execRan = true
		return AgentExecResult{}, nil
	}}
	if _, err := backend.CallEnv(context.Background(), e, definition.Tools[0], nil, nil); !errors.Is(err, ErrIsolation) || execRan {
		t.Fatalf("sandboxed call hit the in-runtime executor: %v", err)
	}
}

// Binding validation rejects each malformed identity/credential combination
// before a record can reach the store.
func TestToolBindingValidateBranches(t *testing.T) {
	valid := ToolBinding{Schema: SchemaVersion, ToolBindingID: DeterministicBindingID("p", "c", "r", "d", "1.0.0", "", ""), PrincipalID: "p", ContextID: "c", RuntimeID: "r", DefinitionID: "d", DefinitionVersion: "1.0.0", PolicyVersion: "policy-1", WorkloadClass: PerUser, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	bad := valid
	bad.Revision = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("zero revision accepted")
	}
	bad = valid
	bad.WorkloadClass = "bogus"
	if err := bad.Validate(); err == nil {
		t.Fatal("bogus workload class accepted")
	}
	bad = valid
	bad.Status = "bogus"
	if err := bad.Validate(); err == nil {
		t.Fatal("bogus status accepted")
	}
	bad = valid
	bad.CredentialRevision = 1
	if err := bad.Validate(); err == nil {
		t.Fatal("credential revision without connection accepted")
	}
	bad = valid
	bad.ConnectionID = "bad id!"
	bad.ConnectionRevision = 1
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid connection id accepted")
	}
	bad = valid
	bad.ConnectionID = DeterministicBindingID("p", "c", "r", "d", "1.0.0", "x", "")
	bad.ConnectionRevision = 0
	if err := bad.Validate(); err == nil {
		t.Fatal("zero connection revision accepted")
	}
	bad = valid
	bad.ConnectionID = "conn-1"
	bad.ConnectionRevision = 1
	bad.CredentialRefID = "bad ref!"
	bad.CredentialRevision = 1
	if err := bad.Validate(); err == nil {
		t.Fatal("invalid credential ref accepted")
	}
}

// The agent backend wiring only activates when the environment names the
// executor channel; unconfigured stays nil so dispatch fails closed.
func TestAgentBackendFromEnv(t *testing.T) {
	t.Setenv("HUB_AGENT_EXEC_CONTAINER", "")
	t.Setenv("HUB_AGENT_EXEC_MODE", "")
	if got := agentBackendFromEnv(); got != nil {
		t.Fatal("backend configured without environment")
	}
	t.Setenv("HUB_AGENT_EXEC_MODE", "supervisor")
	if got := agentBackendFromEnv(); got == nil {
		t.Fatal("supervisor mode did not wire the docker channel")
	}
	t.Setenv("HUB_AGENT_EXEC_MODE", "")
	t.Setenv("HUB_AGENT_EXEC_CONTAINER", "fixed-runtime")
	if got := agentBackendFromEnv(); got == nil {
		t.Fatal("fixed container did not wire the docker channel")
	}
}

func TestListenHelpers(t *testing.T) {
	if got := formOriginForListen("127.0.0.1:9000"); got != "http://127.0.0.1:9000" {
		t.Fatal(got)
	}
	if got := formOriginForListen("0.0.0.0:9000/"); got != "http://127.0.0.1:9000" {
		t.Fatal(got)
	}
	if got := formOriginForListen(""); got != "http://127.0.0.1" {
		t.Fatal(got)
	}
	if got := formOriginForListen("https://hub.example/x/"); got != "https://hub.example/x" {
		t.Fatal(got)
	}
	if !nonLoopbackListen("0.0.0.0:9000") || nonLoopbackListen("127.0.0.1:9000") || nonLoopbackListen("bad") {
		t.Fatal("nonLoopbackListen misclassified")
	}
	if !nonLoopbackListen(":9000") || nonLoopbackListen("localhost:9000") {
		t.Fatal("nonLoopbackListen host-edge misclassified")
	}
	if hostOfURL("::::") != "" || hostOfURL("https://EXAMPLE.com:8443/x") != "example.com" {
		t.Fatal("hostOfURL")
	}
	if !hasMountSource([]Mount{{Source: "a"}}, "a") || hasMountSource([]Mount{{Source: "a"}}, "b") {
		t.Fatal("hasMountSource")
	}
	if envOr("DEVCHECK_TEST_MISSING_ENV", "fallback") != "fallback" {
		t.Fatal("envOr fallback")
	}
	t.Setenv("DEVCHECK_TEST_SET_ENV", "value")
	if envOr("DEVCHECK_TEST_SET_ENV", "fallback") != "value" {
		t.Fatal("envOr set")
	}
}

func TestTinyHelpers(t *testing.T) {
	long := strings.Repeat("x", 300)
	if got := shortErr([]byte(long)); len(got) != 200 {
		t.Fatal("shortErr did not truncate")
	}
	env := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if !(OwnerRef{Type: PrincipalOwner, ID: env.PrincipalID}).Matches(env) {
		t.Fatal("principal owner mismatch")
	}
	if !(OwnerRef{Type: ContextOwner, ID: env.ContextID}).Matches(env) {
		t.Fatal("context owner mismatch")
	}
	if (OwnerRef{Type: "other", ID: env.ContextID}).Matches(env) {
		t.Fatal("unknown owner type matched")
	}
}

func TestProjectionEndpointRefresh(t *testing.T) {
	ep := &projectionEndpoint{gateway: &Gateway{Store: NewStore(), AuditWrite: func(string, map[string]string) error { return nil }}}
	_ = ep.RefreshProjection()
}
