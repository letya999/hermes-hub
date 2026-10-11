package toolhub

// Bounded-cli milestone coverage: shipped catalog validity, plan approval
// shapes, runner→controller request/receipt strictness and the prepare_cli
// control path — grants, spec denials, owner scope. Cell lifecycle coverage
// lives in cell_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLICatalogDefinitionsValidate(t *testing.T) {
	definitions := CLICatalogDefinitions()
	if len(definitions) < 2 {
		t.Fatalf("catalog too small: %d", len(definitions))
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.Transport != BoundedCLI || definition.Workload.Class != PerUser {
			t.Fatalf("catalog definition %s is not a per-user bounded CLI", definition.DefinitionID)
		}
		if err := definition.Validate(); err != nil {
			t.Fatalf("catalog definition %s invalid: %v", definition.DefinitionID, err)
		}
		if seen[definition.DefinitionID] {
			t.Fatalf("duplicate catalog id %s", definition.DefinitionID)
		}
		seen[definition.DefinitionID] = true
	}
	for _, want := range []string{"cli-git-ls-remote", "cli-rg-search"} {
		if !seen[want] {
			t.Fatalf("missing catalog definition %s", want)
		}
	}
}

func TestCLIReceiptStrictness(t *testing.T) {
	effective := EffectiveBinding{WorkloadID: "w-cli", Definition: boundedCLIDefinition()}
	base := AdmissionReceipt{WorkloadID: "w-cli", State: "running", Enforced: true, Isolation: []string{"filesystem", "network", "pid"}, Execution: effective.Definition.Execution, CellID: "cli-abc123", Runtime: "runc"}
	if err := base.validate(effective); err != nil {
		t.Fatalf("valid CLI receipt denied: %v", err)
	}
	for _, drop := range []string{"filesystem", "network", "pid"} {
		receipt := base
		receipt.Isolation = nil
		for _, scope := range []string{"filesystem", "network", "pid"} {
			if scope != drop {
				receipt.Isolation = append(receipt.Isolation, scope)
			}
		}
		if err := receipt.validate(effective); err == nil {
			t.Fatalf("receipt missing %s isolation accepted", drop)
		}
	}
	receipt := base
	receipt.CellID = ""
	if err := receipt.validate(effective); err == nil {
		t.Fatal("CLI receipt without a cell identity accepted")
	}
	receipt = base
	receipt.Runtime = ""
	if err := receipt.validate(effective); err == nil {
		t.Fatal("CLI receipt without a runtime tier accepted")
	}
	receipt = base
	receipt.Runtime = "host"
	if err := receipt.validate(effective); err == nil {
		t.Fatal("CLI receipt with a foreign runtime accepted")
	}
	receipt = base
	receipt.ImageDigest = "sha256:" + strings.Repeat("1", 64)
	if err := receipt.validate(effective); err == nil {
		t.Fatal("CLI receipt with a container field accepted")
	}
	receipt = base
	receipt.Enforced = false
	if err := receipt.validate(effective); err == nil {
		t.Fatal("unenforced CLI receipt accepted")
	}
	receipt = base
	receipt.Execution.TimeoutSeconds++
	if err := receipt.validate(effective); err == nil {
		t.Fatal("drifted execution receipt accepted")
	}
	// Artifact plans require the receipt to attest the pinned image digest.
	artifact := effective
	artifact.Definition.Source = DefinitionSource{Image: "ghcr.io/example/tool", Digest: "sha256:" + strings.Repeat("9", 64), Command: "/bin/tool"}
	receipt = base
	receipt.ImageDigest = "sha256:" + strings.Repeat("9", 64)
	if err := receipt.validate(artifact); err != nil {
		t.Fatalf("matching artifact receipt denied: %v", err)
	}
	receipt.ImageDigest = "sha256:" + strings.Repeat("1", 64)
	if err := receipt.validate(artifact); err == nil {
		t.Fatal("artifact digest mismatch accepted")
	}
}

func TestPlanApprovedShapes(t *testing.T) {
	c, _ := cellFixture(t, nil)
	cfg := c.config.CLI
	// The shipped approval tuple is attested exactly.
	approved := controllerPlan{DefinitionID: "cli-git-ls-remote", DefinitionVersion: "1.0.1", Execution: cfg.Approved[0].Execution}
	if !planApproved(cfg, approved, "git") {
		t.Fatal("shipped approval tuple denied")
	}
	// Definition-id, command or execution drift on the tuple denies.
	drifted := approved
	drifted.Execution.TimeoutSeconds++
	if planApproved(cfg, drifted, "git") {
		t.Fatal("execution drift on approved tuple admitted")
	}
	// A different command than the approved tuple is a different plan shape —
	// "rm" is in neither the tuple nor UserCommands.
	if planApproved(cfg, approved, "rm") {
		t.Fatal("unlisted command admitted through the approved tuple")
	}
	// Catalog commands come from UserCommands only.
	if !planApproved(cfg, controllerPlan{}, "rg") || planApproved(cfg, controllerPlan{}, "curl") {
		t.Fatal("user command shape wrong")
	}
	// Artifact plans admit on a digest-pinned image plus absolute guest path.
	artifact := controllerPlan{Image: "ghcr.io/example/tool", Digest: "sha256:" + strings.Repeat("9", 64)}
	if !planApproved(cfg, artifact, "/usr/bin/tool") {
		t.Fatal("pinned artifact plan denied")
	}
	if planApproved(cfg, artifact, "tool") {
		t.Fatal("bare artifact command admitted")
	}
	mutable := controllerPlan{Image: "ghcr.io/example/tool:latest"}
	if planApproved(cfg, mutable, "/usr/bin/tool") {
		t.Fatal("mutable artifact image admitted")
	}
}

func TestValidateCLIConfigBounds(t *testing.T) {
	if err := validateCLIConfig(nil); err != nil {
		t.Fatalf("nil config denied: %v", err)
	}
	cellinit := filepath.Join(t.TempDir(), "cellinit")
	if err := os.WriteFile(cellinit, []byte("x"), 0700); err != nil {
		t.Fatal(err)
	}
	valid := func() *CLIControllerConfig {
		return &CLIControllerConfig{
			ToolsImage: "hermes-hub-cli-tools:test", CellInit: cellinit, AllowedEgress: []string{"127.0.0.1"},
			Ceiling:  cellExecution(),
			Approved: []CLIApproval{{DefinitionID: "cli-x", DefinitionVersion: "1.0.0", Command: "rg", Execution: ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1024, CPUMillis: 100, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"127.0.0.1"}}}},
		}
	}
	if err := validateCLIConfig(valid()); err != nil {
		t.Fatalf("valid config denied: %v", err)
	}
	cfg := valid()
	cfg.ToolsImage = "bad image with spaces"
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("invalid tools image accepted")
	}
	cfg = valid()
	cfg.CellInit = "relative/cellinit"
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("relative cellinit accepted")
	}
	cfg = valid()
	cfg.Runtime = "not-a-runtime"
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("unknown runtime accepted")
	}
	cfg = valid()
	cfg.AllowedEgress = nil
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("empty egress allowlist accepted")
	}
	cfg = valid()
	cfg.Approved = nil
	cfg.UserCommands = nil
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("empty approval surface accepted")
	}
	cfg = valid()
	cfg.Approved[0].Image = "ghcr.io/example/tool"
	cfg.Approved[0].Digest = "sha256:" + strings.Repeat("1", 64)
	cfg.Approved[0].Command = "tool" // artifact approvals need a guest path
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("artifact approval without guest path accepted")
	}
	cfg = valid()
	cfg.Toolboxes = map[string][]string{"box": {"ghcr.io/example/tool:latest"}}
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("unpinned toolbox member accepted")
	}
	cfg = valid()
	cfg.PoolSize = 99
	if err := validateCLIConfig(cfg); err == nil {
		t.Fatal("unbounded pool accepted")
	}
}

// runnerExecStub answers the runner's Exec seam; receipt defaults to a valid
// cell attestation for the call's workload and execution plan.
func runnerExecStub(t *testing.T, hook func(*cliExecRequest)) (CLIExecFunc, *cliExecRequest) {
	t.Helper()
	var captured cliExecRequest
	return func(ctx context.Context, req cliExecRequest) (cliExecResponse, error) {
		captured = req
		if hook != nil {
			hook(&req)
		}
		return cliExecResponse{
			Receipt: AdmissionReceipt{
				WorkloadID: req.Plan.WorkloadID, State: "running", Enforced: true,
				Isolation: []string{"filesystem", "network", "pid"},
				Execution: req.Plan.Execution, CellID: "cli-test", Runtime: "runc",
				ImageDigest: req.Plan.Digest,
			},
			Output: "ok",
		}, nil
	}, &captured
}

// cliCallEffective builds a resolvable per-user binding for runner calls.
func cliCallEffective(definition ToolDefinition) EffectiveBinding {
	return EffectiveBinding{
		WorkloadID: "w-cli-1",
		Binding:    ToolBinding{PrincipalID: "alice", ContextID: "ctx-alice", ToolBindingID: "bind-alice"},
		Definition: definition,
	}
}

func TestCLIRunnerArtifactReceipt(t *testing.T) {
	root := t.TempDir()
	definition := testCLIDefinition("/usr/bin/mytool", []string{"--serve"})
	definition.Workload.Class = PerUser
	definition.Source.Image = "ghcr.io/example/mytool"
	definition.Source.Digest = "sha256:" + strings.Repeat("9", 64)
	definition.Source.Repository = "https://github.com/example/tool"
	definition.Source.CommitSHA = strings.Repeat("a", 40)
	definition.Source.ArchiveDigest = "sha256:" + strings.Repeat("1", 64)
	definition.Source.RecipeDigest = "sha256:" + strings.Repeat("2", 64)
	definition.Source.ReviewDigest = "sha256:" + strings.Repeat("3", 64)
	if err := definition.Validate(); err != nil {
		t.Fatalf("artifact definition invalid: %v", err)
	}
	effective := cliCallEffective(definition)
	// A receipt that attests the pinned image digest admits.
	execFn, captured := runnerExecStub(t, nil)
	runner := CLIRunner{Root: root, Exec: execFn}
	result, err := runner.Call(context.Background(), effective, definition.Tools[0], nil)
	if err != nil || result.IsError {
		t.Fatalf("artifact call denied: %v", err)
	}
	if captured.Plan.Image != definition.Source.Image || captured.Plan.Digest != definition.Source.Digest || captured.Command != "/usr/bin/mytool" {
		t.Fatalf("exec request=%+v", captured)
	}
	// A receipt attesting the wrong image digest denies.
	execFn, _ = runnerExecStub(t, nil)
	runner = CLIRunner{Root: root, Exec: func(ctx context.Context, req cliExecRequest) (cliExecResponse, error) {
		response, _ := execFn(ctx, req)
		response.Receipt.ImageDigest = "sha256:" + strings.Repeat("f", 64)
		return response, nil
	}}
	if _, err := runner.Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("drifted artifact receipt admitted: %v", err)
	}
	// A receipt missing the cell identity denies.
	runner.Exec = func(ctx context.Context, req cliExecRequest) (cliExecResponse, error) {
		response, _ := execFn(ctx, req)
		response.Receipt.CellID = ""
		return response, nil
	}
	if _, err := runner.Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("cell-less receipt admitted: %v", err)
	}
}

func TestBoundedCLIArtifactSourceValidation(t *testing.T) {
	definition := boundedCLIDefinition()
	definition.Source.Command = "not/abs/path"
	definition.Source.Digest = "sha256:" + strings.Repeat("a", 64)
	if err := definition.Validate(); err == nil {
		t.Fatal("provenance without an absolute command accepted")
	}
	definition = boundedCLIDefinition()
	definition.Source.Command = "/abs/tool"
	definition.Source.Repository = "https://github.com/example/tool"
	if err := definition.Validate(); err == nil {
		t.Fatal("partial provenance chain accepted")
	}
	definition.Source.Repository = "https://github.com/example/tool"
	definition.Source.CommitSHA = "not-a-sha"
	definition.Source.Digest = "sha256:" + strings.Repeat("a", 64)
	definition.Source.ArchiveDigest = "sha256:" + strings.Repeat("1", 64)
	definition.Source.RecipeDigest = "sha256:" + strings.Repeat("2", 64)
	definition.Source.ReviewDigest = "sha256:" + strings.Repeat("3", 64)
	if err := definition.Validate(); err == nil {
		t.Fatal("mutable commit reference accepted")
	}
}

// controlCLIFixture wires a control plane with the prepare op granted and a
// self-install grant only when requested.
func controlCLIFixture(t *testing.T, selfInstall bool) *controlFixture {
	t.Helper()
	// rg is the only owner-registrable command; git deliberately absent.
	t.Setenv("HUB_CLI_ALLOWLIST", "rg")
	fix := newControlFixture(t, func(context.Context, ArtifactSource, *RecipeCandidate) (SourceReview, error) {
		return SourceReview{}, fmt.Errorf("reviewer must not run for cli")
	})
	fix.control.CLIArtifacts = nil
	if selfInstall {
		if err := putGrant(t, fix.store, OperatorGrant(GrantSelfInstall, "alice", "", "")); err != nil {
			t.Fatal(err)
		}
	}
	return fix
}

func TestPrepareCLIGrantAndAllowlist(t *testing.T) {
	spec := map[string]any{
		"name": "owner-search", "command": "rg", "args": []any{"--no-heading"},
		"tools": []any{map[string]any{"name": "search", "effect": "read"}},
	}
	// Without the self-install grant the op denies before touching the spec.
	fix := controlCLIFixture(t, false)
	if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "k1"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("missing self-install grant admitted: %v", err)
	}
	fix = controlCLIFixture(t, true)
	// git is intentionally not owner-registrable: hooks execute code.
	git := map[string]any{"name": "owner-git", "command": "git", "tools": []any{map[string]any{"name": "g", "effect": "read"}}}
	if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": git, "request_key": "k2"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("git owner registration admitted: %v", err)
	}
	// Shell names (not operator-allowlisted here), absolute paths and
	// exec-capable interpreters deny.
	for _, command := range []string{"bash", "sh", "python3", "/usr/bin/rg", "rg;rm", "rg x", "rg\nid"} {
		bad := map[string]any{"name": "owner-x", "command": command, "tools": []any{map[string]any{"name": "g", "effect": "read"}}}
		if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": bad, "request_key": "k3"}); err == nil {
			t.Fatalf("command %q admitted", command)
		}
	}
}

// TestPrepareCLIShellAllowlisted pins the operator opt-in: a bare shell name
// validates once the owner allowlist carries it, while a shell guest path on
// an image plan still fails structural validation.
func TestPrepareCLIShellAllowlisted(t *testing.T) {
	fix := controlCLIFixture(t, true)
	t.Setenv("HUB_CLI_ALLOWLIST", "rg,bash")
	spec := map[string]any{
		"name": "owner-shell", "command": "bash", "args": []any{"-c"},
		"tools": []any{map[string]any{"name": "run", "effect": "write",
			"arguments": []any{map[string]any{"name": "command", "type": "string", "required": true, "pattern": `(?s).{1,1000}`}}}},
	}
	body, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "sh1"})
	if err != nil {
		t.Fatalf("allowlisted bash spec rejected: %v", err)
	}
	if phase, _ := body["phase"].(string); phase != "awaiting-confirm" {
		t.Fatalf("shell spec phase %q", phase)
	}
	// Artifact guest paths named like shells stay denied: no allowlist can
	// smuggle a shell in through the pinned-image channel.
	artifact := ToolDefinition{Schema: SchemaVersion, DefinitionID: "bash-img", Version: "1.0.0", Transport: BoundedCLI,
		Source: DefinitionSource{Command: "/usr/local/bin/bash", Image: "img", Digest: "sha256:" + strings.Repeat("a", 64),
			Repository: "https://github.com/x/y", ArchiveDigest: "sha256:" + strings.Repeat("b", 64), RecipeDigest: "sha256:" + strings.Repeat("c", 64), ReviewDigest: "sha256:" + strings.Repeat("d", 64)},
		Tools: []ToolSpec{{Name: "run", Effect: WriteEffect}}}
	if err := artifact.Validate(); err == nil {
		t.Fatal("shell guest path on image plan validated")
	}
}

func TestPrepareCLISpecStrictness(t *testing.T) {
	fix := controlCLIFixture(t, true)
	invoke := func(spec map[string]any) error {
		_, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "k"})
		return err
	}
	base := func() map[string]any {
		return map[string]any{
			"name": "owner-search", "command": "rg", "args": []any{"--no-heading"},
			"tools": []any{map[string]any{
				"name": "search", "effect": "read",
				"arguments": []any{map[string]any{"name": "pattern", "type": "string", "required": true, "flag": "--regexp"}},
			}},
		}
	}
	// Unknown spec keys deny — no silent passthrough.
	bad := base()
	bad["shell"] = true
	if err := invoke(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown spec key admitted: %v", err)
	}
	// rg --pre/--pre-glob can spawn a preprocessor; file-operand flags read
	// outside the workspace; bare tokens are positional path operands. All deny.
	bad = base()
	bad["tools"] = []any{map[string]any{"name": "search", "effect": "read", "arguments": []any{map[string]any{"name": "pre", "type": "string", "flag": "--pre"}}}}
	if err := invoke(bad); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("--pre flag admitted: %v", err)
	}
	bad = base()
	bad["args"] = []any{"--pre", "sort"}
	if err := invoke(bad); err == nil {
		t.Fatalf("--pre fixed arg admitted: %v", err)
	}
	bad = base()
	bad["args"] = []any{"--file=/etc/passwd"}
	if err := invoke(bad); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("--file operand admitted: %v", err)
	}
	bad = base()
	bad["args"] = []any{"../escape"}
	if err := invoke(bad); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("path operand admitted: %v", err)
	}
	bad = base()
	bad["args"] = []any{"200"} // flag values use --flag=value form; a bare token is a path operand.
	if err := invoke(bad); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bare value token admitted: %v", err)
	}
	// Duplicate tool names, invalid names, missing tools deny.
	bad = base()
	bad["name"] = "Owner Search!"
	if err := invoke(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid name admitted: %v", err)
	}
	bad = base()
	bad["tools"] = []any{}
	if err := invoke(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty tools admitted: %v", err)
	}
	// Credential names must be env-shaped.
	bad = base()
	bad["credentials"] = []any{"not-a-cred"}
	if err := invoke(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid credential name admitted: %v", err)
	}
	// A valid rg spec registers the immutable definition, publishes it to the
	// owner only, and lands in awaiting-confirm.
	body, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": base(), "request_key": "owner-cli-1"})
	if err != nil {
		t.Fatalf("owner rg spec denied: %v", err)
	}
	onboarding := mustOnboarding(t, fix.store, body["onboarding_id"].(string))
	if onboarding.Phase != PhaseAwaitingConfirm || onboarding.Mode != OnboardingSelfInstall {
		t.Fatalf("onboarding=%+v", onboarding)
	}
	definition, err := fix.store.Definition("owner-search", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Transport != BoundedCLI || definition.Source.Command != "rg" || definition.Workload.Class != PerUser {
		t.Fatalf("registered definition=%+v", definition)
	}
	pub := fix.store.publicationFor(definition)
	if pub.Visibility != PublicationUser || pub.OwnerPrincipalID != "alice" {
		t.Fatalf("publication=%+v", pub)
	}
	// The owner-scoped definition is invisible to another principal.
	if err := fix.store.RequireCatalogAccess(bobAuth(), definition); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bob saw alice's owner definition: %v", err)
	}
	if err := fix.store.RequireCatalogAccess(aliceAuth(), definition); err != nil {
		t.Fatalf("alice lost her own definition: %v", err)
	}
}

func TestPrepareCLIArtifactFailsClosedWithoutPipeline(t *testing.T) {
	fix := controlCLIFixture(t, true)
	spec := map[string]any{
		"name": "owner-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/mytool",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "art-1"}); !errors.Is(err, ErrIsolation) {
		t.Fatalf("nil artifact pipeline admitted: %v", err)
	}
}

func TestPrepareCLIArtifactImmutableSourceOnly(t *testing.T) {
	fix := controlCLIFixture(t, true)
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
			return ImportedArtifact{}, errors.New("must not build")
		},
	}
	// A bare repository URL has no commit pin; nothing mutable reaches Build.
	spec := map[string]any{
		"name": "owner-tool", "source": "https://github.com/example/tool", "binary": "/usr/local/bin/tool",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "art-2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutable source admitted: %v", err)
	}
	// The direct-command fields must not mix with the artifact shape.
	spec = map[string]any{
		"name": "owner-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/tool", "command": "rg",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	if _, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "art-3"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("command+artifact mix admitted: %v", err)
	}
}

func TestPrepareCLIArtifactHappyPath(t *testing.T) {
	fix := controlCLIFixture(t, true)
	var gotSource ArtifactSource
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(_ context.Context, source ArtifactSource, config ArtifactImportConfig) (ImportedArtifact, error) {
			gotSource = source
			if config.Execution.MaxPIDs != 128 || config.Workload.Class != PerUser {
				t.Fatalf("import config not bounded: %+v", config)
			}
			return ImportedArtifact{
				Definition: ToolDefinition{Source: DefinitionSource{Image: "hermes-cli-artifact/owner-tool", RecipeDigest: "sha256:" + strings.Repeat("4", 64), ReviewDigest: "sha256:" + strings.Repeat("5", 64)}},
				Artifact: StoredOCIArtifact{
					ArchiveDigest: "sha256:" + strings.Repeat("1", 64),
					Evidence:      OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("9", 64), ProvenanceDigest: "sha256:" + strings.Repeat("2", 64), SBOMDigest: "sha256:" + strings.Repeat("3", 64)},
				},
			}, nil
		},
	}
	spec := map[string]any{
		"name": "owner-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/mytool",
		"args": []any{"--serve"}, "egress": []any{"api.example.com"},
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	body, err := fix.control.Invoke(context.Background(), aliceAuth(), "prepare_source", map[string]any{"cli": spec, "request_key": "art-4"})
	if err != nil {
		t.Fatalf("artifact prepare denied: %v", err)
	}
	if gotSource.CommitSHA == "" || gotSource.Repository == "" {
		t.Fatalf("source was not pinned: %+v", gotSource)
	}
	onboarding := mustOnboarding(t, fix.store, body["onboarding_id"].(string))
	definition, err := fix.store.Definition("owner-tool", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := definition.Validate(); err != nil {
		t.Fatalf("registered artifact definition invalid: %v", err)
	}
	// The cell executes the verified image itself: the definition carries the
	// image, the manifest digest pin and the declared binary's guest path.
	if definition.Source.Image != "hermes-cli-artifact/owner-tool" || definition.Source.Digest != "sha256:"+strings.Repeat("9", 64) || definition.Source.Command != "/usr/local/bin/mytool" {
		t.Fatalf("artifact source=%+v", definition.Source)
	}
	if definition.Source.CommitSHA != gotSource.CommitSHA || definition.Source.ArchiveDigest == "" || definition.Source.RecipeDigest == "" || definition.Source.ReviewDigest == "" {
		t.Fatalf("provenance chain incomplete: %+v", definition.Source)
	}
	pub := fix.store.publicationFor(definition)
	if pub.Visibility != PublicationUser || pub.OwnerPrincipalID != "alice" {
		t.Fatalf("publication=%+v", pub)
	}
	if onboarding.Phase != PhaseAwaitingConfirm && onboarding.Phase != PhaseAwaitingCreds {
		t.Fatalf("onboarding phase=%s", onboarding.Phase)
	}
}

func TestCLIRunnerFailsClosedWithoutController(t *testing.T) {
	root := t.TempDir()
	definition := testCLIDefinition("rg", nil)
	definition.Workload.Class = PerUser
	effective := cliCallEffective(definition)
	// No exec channel wired: the runner denies rather than running locally.
	runner := CLIRunner{Root: root}
	if _, err := runner.Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("controller-less CLI call ran: %v", err)
	}
	// A non-CLI definition never reaches the exec channel.
	runner.Exec = func(context.Context, cliExecRequest) (cliExecResponse, error) {
		t.Fatal("exec reached for a remote definition")
		return cliExecResponse{}, nil
	}
	if _, err := runner.Call(context.Background(), EffectiveBinding{Definition: remoteDefinition()}, remoteDefinition().Tools[0], nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("remote transport reached CLI runner: %v", err)
	}
	// Controller-side failures stay isolation errors.
	runner.Exec = func(context.Context, cliExecRequest) (cliExecResponse, error) {
		return cliExecResponse{}, errors.New("controller refused")
	}
	if _, err := runner.Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("controller failure surfaced untyped: %v", err)
	}
}

func TestOwnerCLIArgumentsStayTyped(t *testing.T) {
	// Typed spec args always render --flag value pairs; a model can never
	// splice argv through argument values.
	spec := map[string]any{
		"name": "owner-search", "command": "rg",
		"args": []any{"--no-heading", "--color=never"},
		"tools": []any{map[string]any{
			"name": "search", "effect": "read",
			"arguments": []any{
				map[string]any{"name": "pattern", "type": "string", "required": true, "flag": "--regexp"},
				map[string]any{"name": "context", "type": "integer", "flag": "--context"},
			},
		}},
	}
	definition, err := cliDefinitionFromSpec(spec, false)
	if err != nil {
		t.Fatal(err)
	}
	args, err := cliArguments(nil, definition.Tools[0], map[string]any{"pattern": "x; rm -rf /", "context": float64(3)})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "--regexp\x00x; rm -rf /") || !strings.Contains(joined, "--context\x003") {
		t.Fatalf("typed args not rendered as flag/value pairs: %v", args)
	}
	if _, err := cliArguments(nil, definition.Tools[0], map[string]any{"context": "3"}); err == nil {
		t.Fatal("string integer coerced")
	}
	if _, err := cliArguments(nil, definition.Tools[0], nil); err == nil {
		t.Fatal("missing required argument accepted")
	}
}

func TestCLIAllowlistParsesCommandNames(t *testing.T) {
	// The allowlist is bare command names only — paths, slashes and junk are
	// dropped so a malformed env cannot widen registration.
	junk := filepath.Join(t.TempDir(), "cli") + "/"
	t.Setenv("HUB_CLI_ALLOWLIST", "git, rg , "+junk+",bad name!,")
	allow := CLIAllowlistFromEnv()
	for _, entry := range []string{"git", "rg"} {
		if !allow[entry] {
			t.Fatalf("missing allowlist entry %q in %v", entry, allow)
		}
	}
	if allow[junk] || len(allow) != 2 {
		t.Fatalf("path/junk entries leaked: %v", allow)
	}
	t.Setenv("HUB_CLI_ALLOWLIST", "")
	if len(CLIAllowlistFromEnv()) != 0 {
		t.Fatal("empty allowlist admitted entries")
	}
	t.Setenv("HUB_CLI_ALLOWLIST", "bad name!")
	if len(CLIAllowlistFromEnv()) != 0 {
		t.Fatal("junk-only allowlist admitted entries")
	}
}

func TestCLIArtifactPipelineFromEnv(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	pipeline, err := CLIArtifactPipelineFromEnv()
	if err != nil || pipeline == nil || pipeline.Build == nil {
		t.Fatalf("pipeline=%+v err=%v", pipeline, err)
	}
	// Both source kinds dispatch through the shared Build closure.
	config := ArtifactImportConfig{DefinitionID: "rg", Version: "1.0.0", Entrypoint: []string{"/usr/local/bin/rg"}}
	release := ArtifactSource{Repository: "https://github.com/o/r", Tag: "1.0"}
	if _, err := pipeline.Build(context.Background(), release, config); !errors.Is(err, ErrInvalid) {
		t.Fatalf("release dispatch: %v", err)
	}
	if _, err := pipeline.Build(context.Background(), ArtifactSource{Repository: "local"}, config); !errors.Is(err, ErrInvalid) {
		t.Fatalf("repository dispatch: %v", err)
	}
}

func TestCLIArtifactSourcePinning(t *testing.T) {
	c := &ControlPlane{Store: NewStore()}
	source, err := c.cliArtifactSource(context.Background(), map[string]any{"source": githubCommitURL()})
	if err != nil || source.CommitSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("pinned source=%+v err=%v", source, err)
	}
	// A bare repository only resolves through the pinned resolver.
	c.SourceResolver = func(_ context.Context, raw string) (ArtifactSource, error) {
		return ArtifactSource{Repository: raw, CommitSHA: strings.Repeat("b", 40)}, nil
	}
	source, err = c.cliArtifactSource(context.Background(), map[string]any{"source": "https://github.com/example/tool"})
	if err != nil || source.CommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("resolved source=%+v err=%v", source, err)
	}
	c.SourceResolver = nil
	if _, err := c.cliArtifactSource(context.Background(), map[string]any{"source": "https://github.com/example/tool"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bare URL without resolver admitted: %v", err)
	}
}

func TestPrepareCLIArtifactSyncAndInFlight(t *testing.T) {
	fix := controlCLIFixture(t, true)
	fix.control.PrepareSyncWindow = -1 // synchronous finish: no background continuation
	var builds int
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
			builds++
			return ImportedArtifact{
				Definition: ToolDefinition{Source: DefinitionSource{Image: "hermes-cli-artifact/owner-tool", RecipeDigest: "sha256:" + strings.Repeat("4", 64), ReviewDigest: "sha256:" + strings.Repeat("5", 64)}},
				Artifact:   StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("1", 64), Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("9", 64)}},
			}, nil
		},
	}
	auth := aliceAuth()
	spec := map[string]any{
		"name": "owner-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/tool",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	body, err := fix.control.prepareCLIArtifact(context.Background(), auth, spec, "dup", false)
	if err != nil {
		t.Fatalf("sync artifact prepare denied: %v", err)
	}
	if builds != 1 {
		t.Fatalf("builds=%d", builds)
	}
	onboarding := mustOnboarding(t, fix.store, body["onboarding_id"].(string))
	if onboarding.Phase != PhaseAwaitingConfirm {
		t.Fatalf("phase=%s", onboarding.Phase)
	}
	// A preparing stub for the same request key reports in-flight without a
	// second build.
	stub := Onboarding{
		Schema: SchemaVersion, OnboardingID: deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, "dup2"),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: OnboardingSelfInstall, Phase: PhasePreparing, DefinitionID: "owner-tool", DefinitionVersion: "1.0.0",
		SourceURL: "https://github.com/example/mcp", CommitSHA: "0123456789abcdef0123456789abcdef01234567",
		IdempotencyKey: "dup2", Revision: 1, CreatedAt: fix.control.now(),
	}
	if _, _, err := fix.store.ClaimPreparingOnboarding(stub, 35*time.Minute, fix.control.now()); err != nil {
		t.Fatal(err)
	}
	body, err = fix.control.prepareCLIArtifact(context.Background(), auth, spec, "dup2", false)
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 || body["phase"] != PhasePreparing {
		t.Fatalf("in-flight prepare rebuilt: builds=%d body=%v", builds, body)
	}
}

func TestCLIExecFromEnvEndToEnd(t *testing.T) {
	// The runner posts the exec request to /cli-exec and accepts the bounded
	// response; the endpoint derives from the shared admission endpoint.
	var got cliExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-exec" || r.Header.Get("Authorization") != "Bearer "+"tok-tok-tok-tok-tok-tok-tok-toktoktok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(cliExecResponse{
			Receipt: AdmissionReceipt{
				WorkloadID: got.Plan.WorkloadID, State: "running", Enforced: true,
				Isolation: []string{"filesystem", "network", "pid"}, Execution: got.Plan.Execution,
				CellID: "cli-1", Runtime: "runc",
			},
			Output: "listed",
		})
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "tok-tok-tok-tok-tok-tok-tok-toktoktok")
	execFn, err := CLIExecFromEnv()
	if err != nil || execFn == nil {
		t.Fatalf("exec=%v err=%v", execFn, err)
	}
	runner := CLIRunner{Root: t.TempDir(), Exec: execFn}
	effective := cliCallEffective(boundedCLIDefinition())
	ctx := withCallCorrelation(context.Background(), callCorrelation{JobID: "job-1"})
	result, err := runner.Call(ctx, effective, effective.Definition.Tools[0], nil)
	if err != nil || result.Text != "listed" {
		t.Fatalf("call result=%+v err=%v", result, err)
	}
	if got.Command != "glab" || got.Plan.DefinitionID != effective.Definition.DefinitionID || got.Principal != "alice" {
		t.Fatalf("request=%+v", got)
	}
	if got.Workspace.Scope != "binding" || got.Workspace.Path == "" {
		t.Fatalf("workspace not bound for per-user cli: %+v", got.Workspace)
	}
	// A controller denial surfaces as an isolation error, not a result.
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "wrong-token-wrong-token-wrong-t")
	if execFn, err = CLIExecFromEnv(); err != nil || execFn == nil {
		t.Fatalf("exec=%v err=%v", execFn, err)
	}
	runner.Exec = execFn
	if _, err := runner.Call(ctx, effective, effective.Definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("denied exec returned: %v", err)
	}
	// No endpoint configured → nil channel → runner fails closed.
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	execFn, err = CLIExecFromEnv()
	if err != nil || execFn != nil {
		t.Fatalf("endpoint-less exec=%v err=%v", execFn, err)
	}
}

func TestCLIReleaseFromEnvEndToEnd(t *testing.T) {
	var got cliReleaseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cli-release" || r.Header.Get("Authorization") != "Bearer "+"tok-tok-tok-tok-tok-tok-tok-toktoktok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", server.URL+"/admit")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_TOKEN", "tok-tok-tok-tok-tok-tok-tok-toktoktok")
	release, err := CLIReleaseFromEnv()
	if err != nil || release == nil {
		t.Fatalf("nil release channel, err=%v", err)
	}
	if err := release(context.Background(), cliReleaseRequest{PrincipalID: "alice", BindingID: "bind-1", Reason: "binding-release"}); err != nil {
		t.Fatalf("release denied: %v", err)
	}
	if got.PrincipalID != "alice" || got.BindingID != "bind-1" || got.Reason == "" {
		t.Fatalf("release request=%+v", got)
	}
	// The job selector travels too: job-end kills only that job's task cells.
	got = cliReleaseRequest{}
	if err := release(context.Background(), cliReleaseRequest{PrincipalID: "alice", JobID: "job-9", Reason: "job-end"}); err != nil {
		t.Fatalf("job release denied: %v", err)
	}
	if got.PrincipalID != "alice" || got.JobID != "job-9" || got.BindingID != "" || got.Reason != "job-end" {
		t.Fatalf("job release request=%+v", got)
	}
}

func TestCLIPositionalArgument(t *testing.T) {
	// A positional operand renders the bare value after the fixed argv and is
	// gated by its mandatory pattern; the catalog git ls-remote uses it for
	// the repository URL.
	gitDef, ok := func() (ToolDefinition, bool) {
		for _, d := range CLICatalogDefinitions() {
			if d.DefinitionID == "cli-git-ls-remote" {
				return d, true
			}
		}
		return ToolDefinition{}, false
	}()
	if !ok {
		t.Fatal("cli-git-ls-remote catalog definition missing")
	}
	git := gitDef.Tools[0]
	args, err := cliArguments(gitDef.Source.Args, git, map[string]any{"repository": "https://github.com/BurntSushi/ripgrep"})
	if err != nil {
		t.Fatal(err)
	}
	last := args[len(args)-1]
	if last != "https://github.com/BurntSushi/ripgrep" {
		t.Fatalf("positional repository not appended: %v", args)
	}
	for _, bad := range []string{
		"https://evil.example.com/x", "file:///etc/passwd", "ssh://git@github.com/x/y",
		"--upload-pack=touch /tmp/pwned", "-oProxyCommand=id", "https://github.com",
	} {
		if _, err := cliArguments(nil, git, map[string]any{"repository": bad}); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("repository %q accepted", bad)
		}
	}
}

func TestCLIPositionalArgumentValidation(t *testing.T) {
	// Definition validation: positional operands must carry a pattern, and a
	// pattern only makes sense on string arguments.
	base := func() ToolDefinition {
		return ToolDefinition{Schema: SchemaVersion, DefinitionID: "d", Version: "1.0.0", Transport: BoundedCLI,
			Source:    DefinitionSource{Command: "git", Args: []string{"ls-remote"}},
			Tools:     []ToolSpec{{Name: "ls-remote", Description: "d", Effect: ReadEffect, CapabilityID: "cli.git", Uses: []CapabilityUse{{Action: "read", Resource: "remotes"}}}},
			Workload:  WorkloadPolicy{Class: PerUser, WorkspaceScope: "none", Rationale: "r"},
			Execution: ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 1024, CPUMillis: 1000, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"github.com"}},
			Health:    HealthProbe{Kind: "exec", Value: "git", TimeoutSeconds: 5}}
	}
	d := base()
	d.Tools[0].Arguments = []CLIArgument{{Name: "repository", Type: "string", Required: true}}
	if err := d.Validate(); err == nil {
		t.Fatal("patternless positional operand validated")
	}
	d = base()
	d.Tools[0].Arguments = []CLIArgument{{Name: "repository", Type: "integer", Required: true, Pattern: "^x$"}}
	if err := d.Validate(); err == nil {
		t.Fatal("non-string patterned argument validated")
	}
	d = base()
	d.Tools[0].Arguments = []CLIArgument{{Name: "repository", Type: "string", Required: true, Pattern: "^(unclosed$"}}
	if err := d.Validate(); err == nil {
		t.Fatal("uncompilable pattern validated")
	}
	d = base()
	d.Tools[0].Arguments = []CLIArgument{{Name: "repository", Type: "string", Required: true, Pattern: `^https://github\.com/`}}
	if err := d.Validate(); err != nil {
		t.Fatalf("patterned positional rejected: %v", err)
	}
}
