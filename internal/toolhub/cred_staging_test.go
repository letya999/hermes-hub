package toolhub

// #287: per-call credential material (credentials.env, <name>.secret) must be
// staged on tmpfs, never on the persistent host disk. HUB_CRED_STAGING selects
// the staging root; both the injector side (MCPBackend.Root) and the plan
// side (admission WorkspacePath) resolve it, and a filesystem scan of the
// persistent state root finds zero credential files for a staged call.
import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestEndpointConfigStagesCredentialsOnTmpfs(t *testing.T) {
	for _, key := range []string{"HUB_TOOLHUB_TOKEN_ENV", "HUB_USER_ID", "HUB_CONTEXT_ID", "HUB_RUNTIME_ID", "HUB_POLICY_VERSION", "HUB_CAPABILITY_MODE", "HUB_CAPABILITY_GENERATION", "HUB_CAPABILITY_PROFILE_ID", "HUB_CAPABILITY_ENVIRONMENT", "HUB_CREDENTIAL_BROKER_CONTROL_URL", "HUB_CREDENTIAL_BROKER_RUNTIME_URL", "HUB_AGENT_EXEC_CONTAINER", "HUB_AGENT_EXEC_MODE", "HUB_TOOLHUB_STORE", "HUB_DOCKER_SCOPE", "HUB_DOCKER_AGENT_NET", "HUB_TOOLHIVE_ADMISSION_ENDPOINT"} {
		t.Setenv(key, "")
	}
	staging, persistent := t.TempDir(), t.TempDir()
	t.Setenv("HUB_CRED_STAGING", staging)
	t.Setenv("HUB_STATE", persistent)
	t.Setenv("HUB_RUNTIME_AUTH", strings.Repeat("a", 32))
	t.Setenv("HUB_USER_ID", "alice")
	cfg, err := EndpointConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	routing, ok := cfg.Backend.(api.RoutingBackend)
	if !ok {
		t.Fatalf("backend type %T", cfg.Backend)
	}
	mcp, ok := routing.MCP.(api.MCPBackend)
	if !ok {
		t.Fatalf("mcp backend type %T", routing.MCP)
	}
	if mcp.Root != staging {
		t.Fatalf("mcp backend stages on persistent root: %q", mcp.Root)
	}
}

func TestRemoveFinalizerWorkspaceClearsCredentialStaging(t *testing.T) {
	staging, persistent := t.TempDir(), t.TempDir()
	t.Setenv("HUB_CRED_STAGING", staging)
	t.Setenv("HUB_STATE", persistent)
	store := NewStore()
	definition := catalogReadDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := aliceAuth()
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	effective := api.EffectiveBinding{Binding: binding, Definition: definition}
	ws, err := api.OpenWorkloadWorkspace(api.CredentialStagingRoot(), effective, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.WriteWorkloadEnvFile(ws.Path, map[string]string{"SERVICE_TOKEN": "secret"}); err != nil {
		t.Fatal(err)
	}
	control := &ControlPlane{Store: store, WorkloadRoot: persistent, CredentialStagingRoot: api.CredentialStagingRoot(), Now: time.Now}
	onboarding := api.Onboarding{OnboardingID: "onb-1", BindingID: binding.ToolBindingID, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, Definition: &definition}
	if err := control.runRemoveFinalizer(context.Background(), onboarding, api.FinalizerWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatal("staging workspace survived the remove finalizer")
	}
}

func TestCredentialStagingLeavesNothingOnPersistentRoot(t *testing.T) {
	staging := t.TempDir()
	persistent := t.TempDir()
	t.Setenv("HUB_CRED_STAGING", staging)
	t.Setenv("HUB_STATE", persistent)

	definition := statefulContainerDefinition()
	effective := api.EffectiveBinding{
		Binding:    api.ToolBinding{ToolBindingID: "bind-staging", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", WorkloadClass: api.PerUser},
		Definition: definition, Connection: &api.Connection{ConnectionID: "stateful", Metadata: map[string]string{}}, WorkloadID: "staging-workload",
	}
	var seenWorkspace string
	backend := api.MCPBackend{Root: api.CredentialStagingRoot(), AdmissionVerifier: func(_ context.Context, got api.EffectiveBinding) (api.AdmissionReceipt, error) {
		workspace, err := api.OpenWorkloadWorkspace(api.CredentialStagingRoot(), got, "")
		if err != nil {
			return api.AdmissionReceipt{}, err
		}
		seenWorkspace = workspace.Path
		if _, err := os.ReadFile(filepath.Join(workspace.Path, "credentials.env")); err != nil {
			return api.AdmissionReceipt{}, err
		}
		return api.AdmissionReceipt{WorkloadID: got.WorkloadID, State: "running", Enforced: true, ImageDigest: definition.Source.Digest, SidecarImages: definition.Workload.SidecarImages, Execution: definition.Execution}, nil
	}}
	if err := backend.EnsureReady(context.Background(), effective, map[string]string{"SERVICE_TOKEN": "staging-secret"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seenWorkspace, staging) {
		t.Fatalf("workspace not under staging root: %s", seenWorkspace)
	}
	scan := func(root string) []string {
		var hits []string
		_ = filepath.Walk(root, func(path string, info os.FileInfo, _ error) error {
			if info == nil || info.IsDir() {
				return nil
			}
			if info.Name() == "credentials.env" || strings.HasSuffix(info.Name(), ".secret") {
				hits = append(hits, path)
			}
			return nil
		})
		return hits
	}
	if hits := scan(persistent); len(hits) > 0 {
		t.Fatalf("credential material on the persistent root: %v", hits)
	}
	if hits := scan(staging); len(hits) > 0 {
		t.Fatalf("credential material survived cleanup on tmpfs: %v", hits)
	}
}
