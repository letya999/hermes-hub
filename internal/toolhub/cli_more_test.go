package toolhub

// Second block of bounded-cli coverage: env parsing, allowlist resolution,
// artifact failure paths and executor inspect errors.

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLISpecEnvironmentAndStrictLists(t *testing.T) {
	if env, err := cliSpecEnvironment(nil); err != nil || env != nil {
		t.Fatalf("nil env=%v err=%v", env, err)
	}
	if _, err := cliSpecEnvironment("nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-map env admitted: %v", err)
	}
	big := map[string]any{}
	for i := 0; i < 33; i++ {
		big[string(rune('A'+i%26))+strings.Repeat("X", i/26+1)] = "v"
	}
	if _, err := cliSpecEnvironment(big); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized env admitted: %v", err)
	}
	for _, tc := range []map[string]any{
		{"not-a-name": "v"},
		{"GOOD_NAME": 42},
		{"GOOD_NAME": ""},
		{"GOOD_NAME": "has ${SECRET} interpolation"},
		{"GOOD_NAME": "has\nnewline"},
	} {
		if _, err := cliSpecEnvironment(tc); !errors.Is(err, ErrInvalid) {
			t.Fatalf("env %v admitted: %v", tc, err)
		}
	}
	if env, err := cliSpecEnvironment(map[string]any{"GOTIFY_URL": "https://x"}); err != nil || env["GOTIFY_URL"] != "https://x" {
		t.Fatalf("valid env denied: env=%v err=%v", env, err)
	}
	if list, err := strictStringList(nil, "args"); err != nil || list != nil {
		t.Fatalf("nil list=%v err=%v", list, err)
	}
	for _, raw := range []any{
		"not-a-list",
		[]any{""},
		[]any{42},
		[]any{"has\nnewline"},
		[]any{strings.Repeat("x", 257)},
		func() []any {
			out := make([]any, 65)
			for i := range out {
				out[i] = "x"
			}
			return out
		}(),
	} {
		if _, err := strictStringList(raw, "args"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("list %v admitted: %v", raw, err)
		}
	}
	if list, err := strictStringList([]string{"--flag"}, "args"); err != nil || len(list) != 1 {
		t.Fatalf("string list denied: %v", err)
	}
}

func TestCLIRegistrationAllowedBranches(t *testing.T) {
	t.Setenv("HUB_CLI_ALLOWLIST", "")
	if err := cliRegistrationAllowed("rg"); !errors.Is(err, ErrIsolation) {
		t.Fatalf("empty allowlist: %v", err)
	}
	t.Setenv("HUB_CLI_ALLOWLIST", "rg")
	if err := cliRegistrationAllowed("rg"); err != nil {
		t.Fatalf("rg denied: %v", err)
	}
	if err := cliRegistrationAllowed("git"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("git admitted: %v", err)
	}
	// Host paths are never registrable commands in the cell model — guest
	// artifact binaries come through prepareCLIArtifact instead.
	if err := cliRegistrationAllowed(filepath.Join(t.TempDir(), "tool")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("path admitted: %v", err)
	}
	if err := cliRegistrationAllowed("/usr/bin/rg"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("absolute path admitted: %v", err)
	}
}

func TestCLIRunnerFromEnvWiring(t *testing.T) {
	t.Setenv("HUB_CLI_ALLOWLIST", "")
	t.Setenv("HUB_TOOLHIVE_ADMISSION_ENDPOINT", "")
	t.Setenv("HUB_STATE", t.TempDir())
	runner, err := CLIRunnerFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if runner.Exec != nil {
		t.Fatalf("unconfigured runner not fail-closed: %+v", runner)
	}
}

func TestPrepareCLIArtifactFailurePaths(t *testing.T) {
	fix := controlCLIFixture(t, true)
	fix.control.PrepareSyncWindow = -1
	auth := aliceAuth()
	spec := map[string]any{
		"name": "owner-tool", "source": githubCommitURL(), "binary": "/usr/local/bin/tool",
		"tools": []any{map[string]any{"name": "run", "effect": "read"}},
	}
	fix.control.CLIArtifacts = &CLIArtifactPipeline{
		Build: func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
			return ImportedArtifact{}, errors.New("build blew up")
		},
	}
	if _, err := fix.control.prepareCLIArtifact(context.Background(), auth, spec, "fail-1", false); err == nil {
		t.Fatal("build failure admitted")
	}
	var found bool
	for _, record := range fix.store.onboardings {
		if record.IdempotencyKey == "fail-1" && record.Phase == PhaseFailed && record.Error != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("failed prepare did not leave a durable failure record")
	}
	fix.control.CLIArtifacts.Build = func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
		return ImportedArtifact{
			Definition: ToolDefinition{Source: DefinitionSource{Image: "hermes-cli-artifact/owner-tool", RecipeDigest: "sha256:" + strings.Repeat("4", 64), ReviewDigest: "sha256:" + strings.Repeat("5", 64)}},
			Artifact:   StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("1", 64), Evidence: OCIArtifactEvidence{ImageManifestDigest: "sha256:" + strings.Repeat("9", 64), ProvenanceDigest: "sha256:" + strings.Repeat("2", 64), SBOMDigest: "sha256:" + strings.Repeat("3", 64)}},
		}, nil
	}
	// A build that returns no usable manifest digest cannot register: the
	// definition validation layer rejects the empty pin.
	fix.control.CLIArtifacts.Build = func(context.Context, ArtifactSource, ArtifactImportConfig) (ImportedArtifact, error) {
		return ImportedArtifact{
			Definition: ToolDefinition{Source: DefinitionSource{Image: "hermes-cli-artifact/owner-tool"}},
			Artifact:   StoredOCIArtifact{ArchiveDigest: "sha256:" + strings.Repeat("1", 64)},
		}, nil
	}
	if _, err := fix.control.prepareCLIArtifact(context.Background(), auth, spec, "fail-2", false); err == nil {
		t.Fatal("unpinned artifact admitted")
	}
}

func TestCellDaemonFailureModes(t *testing.T) {
	// Docker unreachable propagates as an isolation failure.
	c, fake := cellFixture(t, nil)
	fake.failDocker = errors.New("socket gone")
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
		t.Fatalf("docker failure admitted: %v", err)
	}
	// An image that does not resolve to an image ID denies before create.
	c, fake = cellFixture(t, nil)
	delete(fake.images, "hermes-hub-cli-tools:test")
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
		t.Fatalf("unresolvable image admitted: %v", err)
	}
	// A create that lands nothing fails the pre-start inspect.
	c, fake = cellFixture(t, nil)
	fake.exec = nil
	original := fake.run
	fakeRun := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if len(args) >= 1 && args[0] == "create" && strings.HasPrefix(args[len(args)-2], "sha256:") {
			return []byte("ok\n"), nil // lie: create returns success but no container
		}
		return original(ctx, binary, args...)
	}
	c.command = fakeRun
	if _, err := c.execCLI(context.Background(), cliReq("rg", cellExecution())); !errors.Is(err, ErrIsolation) {
		t.Fatalf("phantom create admitted: %v", err)
	}
}

func TestCLISpecToolEdgeCases(t *testing.T) {
	if _, err := cliSpecTools(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil tools admitted: %v", err)
	}
	if _, err := cliSpecTools("nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-list tools admitted: %v", err)
	}
	if _, err := cliSpecTools([]any{"not-a-map"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-map tool admitted: %v", err)
	}
	if _, err := cliSpecTools([]any{map[string]any{"name": "t", "hack": true}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown tool field admitted: %v", err)
	}
	if _, err := cliSpecTools([]any{map[string]any{"name": "t", "effect": "delete"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad effect admitted: %v", err)
	}
	if _, err := cliSpecArguments("nope"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-list arguments admitted: %v", err)
	}
	if _, err := cliSpecArguments([]any{42}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-map argument admitted: %v", err)
	}
	if _, err := cliSpecArguments([]any{map[string]any{"name": "a", "unknown": 1}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown argument field admitted: %v", err)
	}
	// A spec-declared positional operand carries its validation regexp into
	// the compiled CLIArgument; without this field owner specs can never
	// satisfy the definition validator's positional-argument pattern rule.
	args, err := cliSpecArguments([]any{map[string]any{"name": "pattern", "type": "string", "pattern": "^[A-Za-z0-9_.-]+$"}})
	if err != nil || len(args) != 1 || args[0].Flag != "" || args[0].Pattern != "^[A-Za-z0-9_.-]+$" {
		t.Fatalf("positional pattern not compiled: %+v err=%v", args, err)
	}
	if args, err := cliSpecArguments(nil); err != nil || args != nil {
		t.Fatalf("nil arguments=%v err=%v", args, err)
	}
	// The spec->definition compile path also covers credentials and runtime_env
	// end-to-end here.
	spec := map[string]any{
		"name": "owner-cli", "command": "rg", "version": "2.0.0",
		"args": []any{"--no-heading"}, "egress": []any{"github.com"}, "credentials": []any{"MY_TOKEN"},
		"runtime_env": map[string]any{"GIT_CONFIG_NOSYSTEM": "1"},
		"tools":       []any{map[string]any{"name": "run", "description": "d", "effect": "write"}},
	}
	definition, err := cliDefinitionFromSpec(spec, false)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Version != "2.0.0" || definition.Credentials[0].Name != "MY_TOKEN" || definition.RuntimeEnvironment["GIT_CONFIG_NOSYSTEM"] != "1" || definition.Tools[0].Effect != WriteEffect {
		t.Fatalf("definition=%+v", definition)
	}
}

// The shipped catalog defs carry capability tuples so managed capability
// profiles can select them — the grants model is not consulted for managed
// runtimes, so a catalog def without Uses could never project there.
func TestCLICatalogToolsCarryCapabilityTuples(t *testing.T) {
	for _, definition := range CLICatalogDefinitions() {
		for _, tool := range definition.Tools {
			if tool.CapabilityID == "" || len(tool.Uses) == 0 {
				t.Fatalf("%s tool %s has no capability tuple; managed profiles cannot select it", definition.DefinitionID, tool.Name)
			}
		}
	}
}

// A managed principal's catalog CLI tool projects once the operator selects
// it: registered def + active binding + selection with the attested digest +
// matching allow/ceiling rules. This is the exact path the shipped catalog
// takes onto a managed runtime's surface.
func TestManagedSelectionProjectsCLICatalogTool(t *testing.T) {
	s, auth, _ := seededStore(t)
	definition := cliRipgrepDefinition()
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion,
		WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	auth.CapabilityProfile, auth.Environment, auth.Generation = "alice-default", "dev", 1
	digest := DefinitionDigest(definition)
	rule := CapabilityRule{CapabilityID: "cli.search", ImplementationDigest: digest, Action: "search", Resource: "files",
		Limits: CapabilityLimits{OutputBytes: definition.Execution.OutputBytes, TimeoutSeconds: definition.Execution.TimeoutSeconds}}
	policy := CapabilityPolicy{Schema: SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{auth.PrincipalID}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "Reviewed synthetic fixture", Status: ActiveStatus,
		Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	profile := CapabilityProfile{Schema: SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "Reviewed synthetic fixture", Status: ActiveStatus,
		Selections: []CapabilitySelection{{CapabilityID: "cli.search", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
			ImplementationDigest: digest, ToolName: "search", Name: "rg_search"}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListProjectedTools(auth)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools {
		if tool.Name == "rg_search" && tool.DefinitionID == definition.DefinitionID && tool.BindingID == binding.ToolBindingID {
			found = true
		}
	}
	if !found {
		t.Fatalf("selected catalog CLI tool not projected: %v", tools)
	}
}
