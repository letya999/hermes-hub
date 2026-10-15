package toolhub

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

import "github.com/letya999/hermes-hub/internal/identity"

func remoteDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: api.RemoteMCP,
		Source:      api.DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []api.ToolSpec{{Name: "search", Effect: api.ReadEffect}, {Name: "send", Effect: api.WriteEffect}},
		Credentials: []api.CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      api.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func statefulContainerDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "stateful-mcp", Version: "2.1.0", Transport: api.ContainerMCP,
		Source: api.DefinitionSource{
			Image:   "ghcr.io/example/stateful-mcp",
			Digest:  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Command: "/app/server",
		},
		Tools:       []api.ToolSpec{{Name: "read", Effect: api.ReadEffect}},
		Credentials: []api.CredentialInput{{Name: "SERVICE_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerUser, Stateful: true, Rationale: "Provider session and durable connector state are user-owned", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Execution: api.ExecutionPolicy{
			TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64,
			Egress: []string{"service.example.com"},
			Mounts: []api.Mount{{Source: "connection-state", Target: "/state", ReadOnly: false}},
		},
		Health: api.HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
}

func boundedCLIDefinition() api.ToolDefinition {
	return api.ToolDefinition{
		Schema: api.SchemaVersion, DefinitionID: "gitlab-cli", Version: "1.0.0", Transport: api.BoundedCLI,
		Source:      api.DefinitionSource{Command: "glab", Args: []string{"mr", "list", "--output", "json"}},
		Credentials: []api.CredentialInput{{Name: "GITLAB_TOKEN", Required: true}},
		Workload:    api.WorkloadPolicy{Class: api.PerJob, Rationale: "CLI process is bounded and cleaned after one job"},
		Execution:   api.ExecutionPolicy{TimeoutSeconds: 45, OutputBytes: 512 << 10, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"gitlab.com"}},
		Health:      api.HealthProbe{Kind: "exec", Value: "glab version", TimeoutSeconds: 5},
		Tools:       []api.ToolSpec{{Name: "list", Effect: api.ReadEffect}},
	}
}

func seededStore(t *testing.T) (*Store, identity.Envelope, api.ToolBinding) {
	t.Helper()
	store := NewStore()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: api.ActiveStatus, Metadata: map[string]string{"account": "alice@example.com"}}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	if err := putGrant(t, store, api.OperatorGrant(api.GrantDefinition, auth.PrincipalID, definition.DefinitionID, definition.Version)); err != nil {
		t.Fatal(err)
	}
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return store, auth, binding
}

func TestDefinitionValidationExamplesAndStrictDecode(t *testing.T) {
	for _, definition := range []api.ToolDefinition{remoteDefinition(), statefulContainerDefinition(), boundedCLIDefinition()} {
		if err := definition.Validate(); err != nil {
			t.Fatalf("%s: %v", definition.DefinitionID, err)
		}
	}
	projected := api.ProjectedToolName("google-work", "1.0.0", "search")
	if projected != api.ProjectedToolName("google-work", "1.0.0", "search") || projected != api.ProjectedToolName("google-work", "2.0.0", "search") {
		t.Fatal("projection naming is not deterministic and version-stable")
	}
	if projected == api.ProjectedToolName("google-work", "1.0.0", "read") || projected == api.ProjectedToolName("google-calendar", "1.0.0", "search") {
		t.Fatal("projection naming does not disambiguate definitions or tools")
	}
	if _, err := api.DecodeDefinition([]byte(`{"schema":1,"definition_id":"x","version":"1.0.0","transport":"remote-mcp","source":{"url":"https://x.example/mcp","tls_mode":"required"},"tools":[{"name":"read","effect":"read"}],"workload":{"class":"shared","rationale":"stateless"},"execution":{"timeout_seconds":1,"output_bytes":1,"cpu_millis":1,"memory_mib":16,"max_pids":1,"egress":["x.example"]},"health":{"kind":"http","value":"/health","timeout_seconds":1},"unknown":true}`)); err == nil {
		t.Fatal("unknown definition field accepted")
	}
	bad := remoteDefinition()
	bad.Source.URL = "http://api.example.com/mcp"
	if err := bad.Validate(); err == nil {
		t.Fatal("insecure remote MCP accepted")
	}
	bad = statefulContainerDefinition()
	bad.Source.Digest = ""
	if err := bad.Validate(); err == nil {
		t.Fatal("mutable container accepted")
	}
	bad = boundedCLIDefinition()
	bad.Execution.TimeoutSeconds = api.MaxExecutionTimeout + 1
	if err := bad.Validate(); err == nil {
		t.Fatal("unbounded CLI accepted")
	}
	bad = remoteDefinition()
	bad.Workload.Class = api.Shared
	bad.Credentials[0].PerRequest = false
	if err := bad.Validate(); err == nil {
		t.Fatal("shared static credential accepted")
	}
	bad = statefulContainerDefinition()
	bad.Execution.Mounts[0].Source = "C:/other-user"
	if err := bad.Validate(); err == nil {
		t.Fatal("host mount accepted")
	}
	bad = statefulContainerDefinition()
	bad.Workload.SidecarImages[0] = "ghcr.io/stacklok/toolhive/egress-proxy:latest"
	if err := bad.Validate(); err == nil {
		t.Fatal("mutable sidecar accepted")
	}
}

func TestDefinitionCredentialGroupValidation(t *testing.T) {
	for _, groups := range [][][]string{
		{{}},
		{{"SLACK_MCP_XOXP_TOKEN", "SLACK_MCP_XOXP_TOKEN"}},
		{{"SLACK_MCP_XOXP_TOKEN"}, {"MISSING_TOKEN"}},
	} {
		d := statefulContainerDefinition()
		d.Credentials = []api.CredentialInput{{Name: "SLACK_MCP_XOXP_TOKEN", Required: true}}
		d.CredentialGroups = groups
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("groups=%v accepted: %v", groups, err)
		}
	}
	d := statefulContainerDefinition()
	d.Credentials = []api.CredentialInput{
		{Name: "SLACK_MCP_XOXP_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXC_TOKEN", Required: true},
		{Name: "SLACK_MCP_XOXD_TOKEN", Required: true},
	}
	d.CredentialGroups = [][]string{{"SLACK_MCP_XOXC_TOKEN", "SLACK_MCP_XOXD_TOKEN"}, {"SLACK_MCP_XOXP_TOKEN"}}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid alternatives rejected: %v", err)
	}
	if !d.CredentialsSatisfied(func(name string) bool { return name == "SLACK_MCP_XOXP_TOKEN" }) ||
		!d.CredentialsSatisfied(func(name string) bool { return name != "SLACK_MCP_XOXP_TOKEN" }) ||
		d.CredentialsSatisfied(func(name string) bool { return name == "SLACK_MCP_XOXC_TOKEN" }) {
		t.Fatal("credential group satisfaction mis-evaluated")
	}
}

func TestDefinitionProxyEnvironmentValidation(t *testing.T) {
	for _, proxy := range [][]string{
		{"SLACK_MCP_URL"},
		{"NO_PROXY"},
		{"EXTERNAL_NO_PROXY"},
		{"A_PROXY", "A_PROXY"},
		{"B1_PROXY", "B2_PROXY", "B3_PROXY", "B4_PROXY", "B5_PROXY", "B6_PROXY", "B7_PROXY", "B8_PROXY", "B9_PROXY"},
	} {
		d := statefulContainerDefinition()
		d.ProxyEnvironment = proxy
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("proxy_environment=%v accepted: %v", proxy, err)
		}
	}
	for _, overlap := range []func(*api.ToolDefinition){
		func(d *api.ToolDefinition) {
			d.Credentials = []api.CredentialInput{{Name: "SLACK_MCP_PROXY", Required: true}}
		},
		func(d *api.ToolDefinition) { d.Environment = []string{"SLACK_MCP_PROXY"} },
		func(d *api.ToolDefinition) {
			d.RuntimeEnvironment = map[string]string{"SLACK_MCP_PROXY": "http://evil"}
		},
	} {
		d := statefulContainerDefinition()
		d.ProxyEnvironment = []string{"SLACK_MCP_PROXY"}
		overlap(&d)
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatal("overlapping proxy environment accepted")
		}
	}
	d := statefulContainerDefinition()
	d.ProxyEnvironment = []string{"SLACK_MCP_PROXY", "UPSTREAM_PROXY"}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid proxy environment rejected: %v", err)
	}
	if args := api.ProxyEnvironmentArgs(d, "http://10.0.0.7:3128"); len(args) != 4 || args[1] != "SLACK_MCP_PROXY=http://10.0.0.7:3128" || args[3] != "UPSTREAM_PROXY=http://10.0.0.7:3128" {
		t.Fatalf("proxy env args=%v", args)
	}
}

func TestDefinitionNetworkTransportValidation(t *testing.T) {
	for _, bad := range []string{"websocket", "grpc", "SSEv2", " stdio"} {
		d := statefulContainerDefinition()
		d.Source.NetworkTransport = bad
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("network_transport %q accepted: %v", bad, err)
		}
	}
	for _, good := range []string{"sse", "http", "streamable-http"} {
		d := statefulContainerDefinition()
		d.Source.NetworkTransport = good
		if err := d.Validate(); err != nil {
			t.Fatalf("network_transport %q rejected: %v", good, err)
		}
	}
	d := statefulContainerDefinition()
	d.Transport = api.RemoteMCP
	d.Source = api.DefinitionSource{URL: "https://mcp.example/endpoint", TLSMode: "required"}
	d.Source.NetworkTransport = "sse"
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("network transport on %s accepted: %v", d.Transport, err)
	}
}

func TestBoundedCLISchemaCoversScalarArgumentTypes(t *testing.T) {
	for _, kind := range []string{"string", "integer", "number", "boolean"} {
		definition := boundedCLIDefinition()
		definition.Tools[0].Arguments = []api.CLIArgument{{Name: "value", Flag: "--value", Type: kind}}
		if err := definition.Validate(); err != nil {
			t.Fatalf("valid CLI type %q rejected: %v", kind, err)
		}
	}
	for _, argument := range []api.CLIArgument{{Name: "value", Flag: "value", Type: "string"}, {Name: "value", Flag: "--value", Type: "object"}} {
		definition := boundedCLIDefinition()
		definition.Tools[0].Arguments = []api.CLIArgument{argument}
		if err := definition.Validate(); err == nil {
			t.Fatalf("invalid CLI argument accepted: %+v", argument)
		}
	}
}

func TestResolveChecksExactOwnerAndCurrentState(t *testing.T) {
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil || effective.Connection == nil || effective.Credential == nil || effective.WorkloadID == "" {
		t.Fatalf("resolve failed: effective=%+v err=%v", effective, err)
	}
	bob := identity.TelegramEnvelope("bob", 8, "runtime-bob", "policy-1")
	if _, err := store.Resolve(bob, binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("cross-owner resolve error=%v", err)
	}
	stalePolicy := auth
	stalePolicy.PolicyVersion = "policy-2"
	if _, err := store.Resolve(stalePolicy, binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("stale policy error=%v", err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrStale) {
		t.Fatalf("rotated credential error=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("disabled binding error=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("revoked binding error=%v", err)
	}
}

func TestConcurrentRevocationCannotPassAdmission(t *testing.T) {
	store, auth, binding := seededStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	authorized := make(chan error, 1)
	go func() {
		authorized <- store.Authorize(auth, binding.ToolBindingID, func(api.EffectiveBinding) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus) }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation overtook admitted call: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-authorized; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("post-admission revocation error=%v", err)
	}
}

func TestConcurrentCredentialRotationAndFileRoundTrip(t *testing.T) {
	store, auth, binding := seededStore(t)
	const rotations = 8
	results := make(chan error, rotations)
	for i := 0; i < rotations; i++ {
		go func(i int) {
			_, err := store.RotateCredential("google-work", "local", "local://alice/google/rotation", []string{"GOOGLE_TOKEN"})
			results <- err
		}(i)
	}
	for i := 0; i < rotations; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrStale) {
		t.Fatalf("old binding survived concurrent rotation: %v", err)
	}
	path := filepath.Join(t.TempDir(), "toolhub.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "super-secret") || !strings.Contains(string(body), "credential_references") {
		t.Fatal("store contains secret material or omitted references")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.Resolve(auth, binding.ToolBindingID); !errors.Is(err, api.ErrStale) {
		t.Fatalf("round-tripped stale binding error=%v", err)
	}
}

func TestWorkloadClassLifecycleModel(t *testing.T) {
	store, _, binding := seededStore(t)
	owner := &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}
	workload, err := api.NewWorkloadInstance(binding, owner, "", 1, time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	jobDefinition := boundedCLIDefinition()
	jobDefinition.Credentials = nil
	jobBinding := binding
	jobBinding.WorkloadClass = api.PerJob
	jobBinding.DefinitionID = "gitlab-cli"
	jobBinding.DefinitionVersion = "1.0.0"
	jobBinding.ConnectionID = ""
	jobBinding.ConnectionRevision = 0
	jobBinding.CredentialRefID = ""
	jobBinding.CredentialRevision = 0
	jobBinding.ToolBindingID = ""
	if err := store.RegisterDefinition(jobDefinition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(jobBinding); err != nil {
		t.Fatal(err)
	}
	jobBinding.ToolBindingID = api.DeterministicBindingID(jobBinding.PrincipalID, jobBinding.ContextID, jobBinding.RuntimeID, jobBinding.DefinitionID, jobBinding.DefinitionVersion, "", "")
	job, err := api.NewWorkloadInstance(jobBinding, owner, "job-1", 1, time.Now(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(job); err != nil {
		t.Fatal(err)
	}
}

func TestAnonymousPerBindingWorkloadIdentity(t *testing.T) {
	store := NewStore()
	definition := remoteDefinition()
	definition.Credentials = nil
	definition.Workload.Stateful = true
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	paths := map[string]bool{}
	root := t.TempDir()
	if _, err := api.OpenWorkloadWorkspace(root, api.EffectiveBinding{Definition: definition, Binding: api.ToolBinding{ContextID: "alice"}}, ""); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("unidentified anonymous workspace accepted: %v", err)
	}
	for i := 0; i < 100; i++ {
		auth := identity.TelegramEnvelope("alice", 7, fmt.Sprintf("runtime-%d", i), "policy-1")
		binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
		if err := store.PutBinding(binding); err != nil {
			t.Fatal(err)
		}
		effective, err := store.Resolve(auth, binding.ToolBindingID)
		if err != nil {
			t.Fatal(err)
		}
		workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, "", 1, time.Now(), time.Time{})
		if err != nil || workload.WorkloadID != effective.WorkloadID || seen[workload.WorkloadID] {
			t.Fatalf("binding %d collided or resolve/create disagree: %v", i, err)
		}
		seen[workload.WorkloadID] = true
		workspace, err := api.OpenWorkloadWorkspace(root, effective, "")
		if err != nil || paths[workspace.Path] {
			t.Fatalf("binding %d shares a state directory: %v", i, err)
		}
		paths[workspace.Path] = true
		if err := os.WriteFile(filepath.Join(workspace.Path, "synthetic-marker"), []byte(auth.RuntimeID), 0600); err != nil {
			t.Fatal(err)
		}
		restarted, err := api.OpenWorkloadWorkspace(root, effective, "")
		if err != nil || restarted.Path != workspace.Path {
			t.Fatalf("restart state moved: %v", err)
		}
		if err := store.PutWorkloadInstance(workload); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Resolve(identity.TelegramEnvelope("bob", 8, auth.RuntimeID, "policy-1"), binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
			t.Fatalf("cross-user resolve: %v", err)
		}
	}
}

func TestCredentialBindingIsolationNinetyFivePlusFive(t *testing.T) {
	definition := remoteDefinition()
	definition.Workload.Stateful = true
	root := t.TempDir()
	workloads := map[string]bool{}
	paths := map[string]bool{}
	for i := 0; i < 100; i++ {
		principal := fmt.Sprintf("user-%03d", i)
		connectionID := fmt.Sprintf("connection-%03d", i)
		credentialID := fmt.Sprintf("credential-%03d", i)
		if i >= 95 {
			connectionID, credentialID = "shared-connection", "shared-credential"
		}
		binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: principal, ContextID: principal, RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connectionID, CredentialRefID: credentialID, PolicyVersion: "policy-1", WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
		binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
		workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: principal}, "", 1, time.Now(), time.Time{})
		if err != nil || workloads[workload.WorkloadID] {
			t.Fatalf("binding %d shares a workload: %v", i, err)
		}
		workloads[workload.WorkloadID] = true
		effective := api.EffectiveBinding{Definition: definition, Binding: binding, Connection: &api.Connection{ConnectionID: connectionID}}
		workspace, err := api.OpenWorkloadWorkspace(root, effective, "")
		if err != nil || paths[workspace.Path] {
			t.Fatalf("binding %d shares state: %v", i, err)
		}
		paths[workspace.Path] = true
	}
	if len(workloads) != 100 || len(paths) != 100 {
		t.Fatalf("got %d workloads and %d state paths", len(workloads), len(paths))
	}
}

func TestStrictValidationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*api.ToolDefinition)
	}{
		{"schema", func(d *api.ToolDefinition) { d.Schema = 2 }},
		{"id", func(d *api.ToolDefinition) { d.DefinitionID = "Bad" }},
		{"version", func(d *api.ToolDefinition) { d.Version = "latest" }},
		{"class", func(d *api.ToolDefinition) { d.Workload.Class = "unknown" }},
		{"rationale", func(d *api.ToolDefinition) { d.Workload.Rationale = " " }},
		{"no tools", func(d *api.ToolDefinition) { d.Tools = nil }},
		{"tool name", func(d *api.ToolDefinition) { d.Tools[0].Name = "Bad Name" }},
		{"tool effect", func(d *api.ToolDefinition) { d.Tools[0].Effect = "delete" }},
		{"duplicate tool", func(d *api.ToolDefinition) { d.Tools = append(d.Tools, d.Tools[0]) }},
		{"credential name", func(d *api.ToolDefinition) { d.Credentials[0].Name = "token" }},
		{"duplicate credential", func(d *api.ToolDefinition) { d.Credentials = append(d.Credentials, d.Credentials[0]) }},
		{"remote TLS", func(d *api.ToolDefinition) { d.Source.TLSMode = "optional" }},
		{"remote credentials in URL", func(d *api.ToolDefinition) {
			d.Source.URL = (&url.URL{Scheme: "https", Host: "api.example.com", Path: "/mcp", User: url.UserPassword("user", "pass")}).String()
		}},
		{"remote query", func(d *api.ToolDefinition) { d.Source.URL = "https://api.example.com/mcp?x=1" }},
		{"remote health kind", func(d *api.ToolDefinition) { d.Health.Kind = "exec" }},
		{"remote health path", func(d *api.ToolDefinition) { d.Health.Value = "health" }},
		{"container tag", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Source.Image = "ghcr.io/example/mcp:latest"
		}},
		{"container command", func(d *api.ToolDefinition) { *d = statefulContainerDefinition(); d.Source.Command = "sh -c" }},
		{"container argument secret", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Source.Args = []string{"${SERVICE_TOKEN}"}
		}},
		{"container health kind", func(d *api.ToolDefinition) { *d = statefulContainerDefinition(); d.Health.Kind = "http" }},
		{"cli command", func(d *api.ToolDefinition) { *d = boundedCLIDefinition(); d.Source.Command = "sh -c" }},
		{"cli source URL", func(d *api.ToolDefinition) { *d = boundedCLIDefinition(); d.Source.URL = "https://gitlab.com" }},
		{"shared state", func(d *api.ToolDefinition) {
			d.Workload.Class = api.Shared
			d.Credentials[0].PerRequest = true
			d.Workload.Stateful = true
		}},
		{"shared static credential", func(d *api.ToolDefinition) { d.Workload.Class = api.Shared }},
		{"per-job state no mount", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Workload.Class = api.PerJob
			d.Execution.Mounts = nil
		}},
		{"bad timeout", func(d *api.ToolDefinition) { d.Execution.TimeoutSeconds = api.MaxExecutionTimeout + 1 }},
		{"bad output", func(d *api.ToolDefinition) { d.Execution.OutputBytes = api.MaxOutputBytes + 1 }},
		{"bad cpu", func(d *api.ToolDefinition) { d.Execution.CPUMillis = api.MaxCPUMillis + 1 }},
		{"bad memory", func(d *api.ToolDefinition) { d.Execution.MemoryMiB = api.MaxMemoryMiB + 1 }},
		{"bad pids", func(d *api.ToolDefinition) { d.Execution.MaxPIDs = api.MaxPIDs + 1 }},
		{"no egress", func(d *api.ToolDefinition) { d.Execution.Egress = nil }},
		{"bad egress", func(d *api.ToolDefinition) { d.Execution.Egress = []string{"https://api.example.com"} }},
		{"duplicate egress", func(d *api.ToolDefinition) { d.Execution.Egress = []string{"api.example.com", "api.example.com"} }},
		{"unsafe mount", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Execution.Mounts[0].Target = "state"
		}},
		{"host mount", func(d *api.ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].Source = "host" }},
		{"readonly connection state", func(d *api.ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].ReadOnly = true }},
		{"wrong workspace mount", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Execution.Mounts[0] = api.Mount{Source: "workspace-readonly", Target: "/workspace", ReadOnly: false}
		}},
		{"wrong job mount", func(d *api.ToolDefinition) {
			*d = remoteDefinition()
			d.Execution.Mounts = []api.Mount{{Source: "job-state", Target: "/state", ReadOnly: false}}
		}},
		{"duplicate mount target", func(d *api.ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Execution.Mounts = append(d.Execution.Mounts, api.Mount{Source: "connection-state", Target: "/state"})
		}},
		{"empty mount source", func(d *api.ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].Source = "" }},
		{"health timeout", func(d *api.ToolDefinition) { d.Health.TimeoutSeconds = 31 }},
		{"health empty", func(d *api.ToolDefinition) { d.Health.Value = "" }},
		{"health newline", func(d *api.ToolDefinition) { d.Health.Value = "/health\n" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			definition := remoteDefinition()
			tc.mutate(&definition)
			if err := definition.Validate(); err == nil {
				t.Fatal("invalid definition accepted")
			}
		})
	}
	if err := (api.ExecutionPolicy{TimeoutSeconds: 1, OutputBytes: 1, CPUMillis: 1, MemoryMiB: 16, MaxPIDs: 1, Egress: []string{"x.example"}, Mounts: []api.Mount{{Source: "x", Target: "/x"}}}).Validate(api.Shared); err == nil {
		t.Fatal("shared host mount accepted")
	}
	base := api.ExecutionPolicy{TimeoutSeconds: 1, OutputBytes: 1, CPUMillis: 1, MemoryMiB: 16, MaxPIDs: 1, Egress: []string{"x.example"}}
	ok := base
	ok.TCPForwards = []api.TCPForward{{Listen: 15432, TargetEnv: "DB_ENDPOINT"}}
	if err := ok.Validate(api.PerUser); err != nil {
		t.Fatalf("valid tcp forward rejected: %v", err)
	}
	for _, forwards := range [][]api.TCPForward{
		{{Listen: 80, TargetEnv: "DB_ENDPOINT"}},
		{{Listen: 70000, TargetEnv: "DB_ENDPOINT"}},
		{{Listen: 15432, TargetEnv: "db_endpoint"}},
		{{Listen: 15432, TargetEnv: "DB_ENDPOINT"}, {Listen: 15432, TargetEnv: "DB2_ENDPOINT"}},
		{{Listen: 15432, TargetEnv: "DB_ENDPOINT"}, {Listen: 15433, TargetEnv: "DB_ENDPOINT"}},
	} {
		bad := base
		bad.TCPForwards = forwards
		if err := bad.Validate(api.PerUser); err == nil {
			t.Fatalf("invalid tcp forwards accepted: %+v", forwards)
		}
	}
}

func TestRecordValidationAndStoreConflicts(t *testing.T) {
	store, _, binding := seededStore(t)
	if err := (api.OwnerRef{Type: "other", ID: "alice"}).Validate(); err == nil {
		t.Fatal("invalid owner accepted")
	}
	if (api.OwnerRef{Type: "other", ID: "alice"}).Matches(identity.TelegramEnvelope("alice", 1, "runtime", "policy-1")) {
		t.Fatal("unknown owner matched")
	}
	definition := remoteDefinition()
	definition.Tools[0].Name = "different"
	if err := store.RegisterDefinition(definition); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("definition conflict=%v", err)
	}
	if err := store.RegisterDefinition(remoteDefinition()); err != nil {
		t.Fatal(err)
	}
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/changed", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	if err := store.PutCredentialReference(credential); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("credential conflict=%v", err)
	}
	badConnection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "other-connection", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: "missing", Revision: 1, Status: api.ActiveStatus}
	if err := store.PutConnection(badConnection); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing connection definition=%v", err)
	}
	badConnection.DefinitionID = "google-work"
	badConnection.CredentialRefID = "missing-credential"
	if err := store.PutConnection(badConnection); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing connection credential=%v", err)
	}
	if err := store.SetConnectionStatus("missing", api.DisabledStatus); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing connection status=%v", err)
	}
	if err := store.SetConnectionStatus("google-work", "bad"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("bad connection status=%v", err)
	}
	if err := store.SetConnectionStatus("google-work", api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionStatus("google-work", api.ActiveStatus); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("revoked connection re-enabled=%v", err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/next", []string{"GOOGLE_TOKEN"}); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("revoked rotation=%v", err)
	}
	if err := store.SetBindingStatus("missing", api.DisabledStatus); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing binding status=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, "bad"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("bad binding status=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, api.ActiveStatus); !errors.Is(err, api.ErrRevoked) {
		t.Fatalf("revoked binding re-enabled=%v", err)
	}
	if err := store.Authorize(identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), binding.ToolBindingID, nil); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("nil authorize callback=%v", err)
	}
}

func TestDecodeLoadAndDenyPaths(t *testing.T) {
	definitionBytes, err := json.Marshal(remoteDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if definition, err := api.DecodeDefinition(definitionBytes); err != nil || definition.DefinitionID != "google-work" {
		t.Fatalf("valid definition decode=%+v err=%v", definition, err)
	}
	for _, body := range [][]byte{[]byte("{"), append(append([]byte(nil), definitionBytes...), []byte("{}")...)} {
		if _, err := api.DecodeDefinition(body); err == nil {
			t.Fatal("malformed definition accepted")
		}
	}
	store, auth, binding := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(path); err != nil || loaded == nil {
		t.Fatalf("valid store load=%v", err)
	}
	for _, body := range []string{`{}`, `{"schema":1,"unknown":true}`} {
		badPath := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(badPath, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(badPath); err == nil {
			t.Fatal("invalid store accepted")
		}
	}
	invalidAuth := auth
	invalidAuth.Schema = 2
	if _, err := store.Resolve(invalidAuth, binding.ToolBindingID); !errors.Is(err, api.ErrUnauthorized) {
		t.Fatalf("invalid auth=%v", err)
	}
	if _, err := store.Resolve(auth, "bad id"); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("invalid binding id=%v", err)
	}
	if _, err := store.Resolve(auth, "missing-binding"); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing binding=%v", err)
	}
	if _, err := Load(filepath.Join("relative", "toolhub.json")); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("relative store path=%v", err)
	}
	symlinkTarget := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(symlinkTarget, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(symlinkTarget, symlinkPath); err == nil {
		if _, err := Load(symlinkPath); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("symlink store path=%v", err)
		}
	}
	if err := store.Authorize(auth, binding.ToolBindingID, func(api.EffectiveBinding) error { return errors.New("backend failed") }); err == nil || err.Error() != "backend failed" {
		t.Fatalf("admission result=%v", err)
	}
}

func TestRecordAndWorkloadDenyPaths(t *testing.T) {
	store, _, binding := seededStore(t)
	credential := api.CredentialReference{Schema: api.SchemaVersion, CredentialRefID: api.CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: api.ActiveStatus}
	credentialCases := []func(*api.CredentialReference){
		func(r *api.CredentialReference) { r.Schema = 2 },
		func(r *api.CredentialReference) { r.CredentialRefID = "bad id" },
		func(r *api.CredentialReference) { r.ConnectionID = "bad id" },
		func(r *api.CredentialReference) { r.Revision = 0 },
		func(r *api.CredentialReference) { r.Backend = "" },
		func(r *api.CredentialReference) { r.Locator = "secret=value" },
		func(r *api.CredentialReference) { r.Keys = nil },
		func(r *api.CredentialReference) { r.Keys = []string{"bad-key"} },
		func(r *api.CredentialReference) { r.Status = api.DisabledStatus },
	}
	for _, mutate := range credentialCases {
		candidate := credential
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid credential reference accepted")
		}
	}
	connection := api.Connection{Schema: api.SchemaVersion, ConnectionID: "google-work", Owner: api.OwnerRef{Type: api.PrincipalOwner, ID: "alice"}, DefinitionID: "google-work", Revision: 1, Status: api.ActiveStatus}
	connectionCases := []func(*api.Connection){
		func(c *api.Connection) { c.Schema = 2 },
		func(c *api.Connection) { c.ConnectionID = "bad id" },
		func(c *api.Connection) { c.DefinitionID = "bad id" },
		func(c *api.Connection) { c.Revision = 0 },
		func(c *api.Connection) { c.Status = "unknown" },
		func(c *api.Connection) { c.Owner = api.OwnerRef{} },
		func(c *api.Connection) { c.CredentialRefID = "bad id" },
		func(c *api.Connection) { c.Metadata = map[string]string{"secret": "no"} },
	}
	for _, mutate := range connectionCases {
		candidate := connection
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	if err := store.PutBinding(api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: "missing", DefinitionVersion: "1.0.0", PolicyVersion: "policy-1", WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}); !errors.Is(err, api.ErrNotFound) {
		t.Fatalf("missing binding definition=%v", err)
	}
	badBinding := binding
	badBinding.ToolBindingID = "manual-binding"
	if err := store.PutBinding(badBinding); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("nondeterministic binding=%v", err)
	}
	badBinding = binding
	badBinding.ToolBindingID = ""
	badBinding.ConnectionRevision++
	if err := store.PutBinding(badBinding); !errors.Is(err, api.ErrStale) {
		t.Fatalf("stale binding revision=%v", err)
	}
	badBinding = binding
	badBinding.ToolBindingID = ""
	badBinding.CredentialRefID = ""
	if err := store.PutBinding(badBinding); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing binding credential=%v", err)
	}
	if err := store.PutWorkloadInstance(api.WorkloadInstance{Schema: api.SchemaVersion, WorkloadID: "work-missing", BindingID: "missing", DefinitionID: "google-work", DefinitionVersion: "1.0.0", Class: api.PerUser, Owner: &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, RuntimeID: "runtime", Generation: 1, Status: api.StartingStatus}); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("missing workload binding=%v", err)
	}
	workload, err := api.NewWorkloadInstance(binding, &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}, "", 1, time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	workload.Status = api.FailedStatus
	if err := store.PutWorkloadInstance(workload); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("mutable workload accepted=%v", err)
	}
	invalidWorkload := workload
	invalidWorkload.Schema = 2
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("invalid workload accepted")
	}
	invalidWorkload = workload
	invalidWorkload.Class = api.Shared
	invalidWorkload.Owner = &api.OwnerRef{Type: api.ContextOwner, ID: "alice"}
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("owned shared workload accepted")
	}
	invalidWorkload = workload
	invalidWorkload.Class = api.PerJob
	invalidWorkload.JobID = "job-1"
	invalidWorkload.ExpiresAt = time.Time{}
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("unbounded per-job workload accepted")
	}
}

// Reload is on the read hot path — the gateway calls it per projected tool —
// so an unchanged on-disk snapshot must short-circuit before the full decode
// and revalidate. The stamp test keeps the file's size and mtime identical
// while replacing its bytes: a skipped Load is the only way garbage parses.
func TestStoreReloadSkipsUnchangedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	writer := NewStore()
	if err := writer.Save(path); err != nil {
		t.Fatal(err)
	}
	store, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.diskStamp.mtime == 0 || store.diskStamp.size == 0 {
		t.Fatal("load did not stamp the snapshot")
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, info.Size()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Unix(0, store.diskStamp.mtime), time.Unix(0, store.diskStamp.mtime)); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("unchanged stamp re-parsed the snapshot: %v", err)
	}
	next := time.Unix(0, store.diskStamp.mtime).Add(time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("changed mtime did not reload the snapshot")
	}
}

// A save through this store updates the stamp in place, so the next Reload
// sees its own write as already applied instead of re-loading it.
func TestStoreSaveStampsSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	store := NewStore()
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	if store.diskStamp.mtime == 0 {
		t.Fatal("save did not stamp the snapshot")
	}
	fresh, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.Reload(); err != nil {
		t.Fatal(err)
	}
}

func TestBrokeredCredentialDeliveryValidation(t *testing.T) {
	brokered := func(target, prefix string) api.CredentialInput {
		return api.CredentialInput{Name: "API_TOKEN", Delivery: "brokered", Target: target, Prefix: prefix}
	}
	// Brokered delivery is bounded-cli only.
	d := remoteDefinition()
	d.Credentials = []api.CredentialInput{brokered("api.example.com", "Bearer ")}
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("brokered on remote-mcp: %v", err)
	}
	// Target must be a bare host — no path or scheme.
	d = boundedCLIDefinition()
	for _, target := range []string{"api.example.com/path", "https://api.example.com", "UPPER host", "api.example.com:8080/extra/seg"} {
		d.Credentials = []api.CredentialInput{brokered(target, "Bearer ")}
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("brokered target %q accepted", target)
		}
	}
	// Prefix length is bounded and cannot carry control bytes.
	d.Credentials = []api.CredentialInput{brokered("api.example.com", strings.Repeat("x", 129))}
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatal("oversized brokered prefix accepted")
	}
	d.Credentials = []api.CredentialInput{brokered("api.example.com", "Bearer \n")}
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatal("control byte in brokered prefix accepted")
	}
	// A well-formed brokered credential validates.
	d.Credentials = []api.CredentialInput{brokered("api.example.com", "Bearer ")}
	if err := d.Validate(); err != nil {
		t.Fatalf("valid brokered credential denied: %v", err)
	}
}

func TestCLICellPolicyValidation(t *testing.T) {
	base := func() api.ToolDefinition {
		d := boundedCLIDefinition()
		d.Workload.Class = api.PerUser
		return d
	}
	// Cell lifecycle fields are bounded-cli only.
	d := remoteDefinition()
	d.Workload.Lifecycle = api.CLILifecycleBinding
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("lifecycle on remote-mcp: %v", err)
	}
	// Package install cannot share stateless cells.
	d = base()
	d.Workload.InstallPackages = true
	d.Workload.Stateless = true
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("installPackages on stateless cell: %v", err)
	}
	// Lifecycle tiers validate their workload class.
	for _, tc := range []struct {
		lifecycle string
		class     api.WorkloadClass
		extra     func(*api.ToolDefinition)
	}{
		{api.CLILifecycleTask, api.PerUser, nil},
		{api.CLILifecycleBinding, api.PerJob, nil},
		{api.CLILifecycleToolbox, api.PerJob, nil},
		{api.CLILifecycleToolbox, api.PerUser, func(d *api.ToolDefinition) { d.Workload.Toolbox = "" }},
		{api.CLILifecycleSharedPool, api.PerUser, func(d *api.ToolDefinition) { d.Workload.Stateless = true }},
		{api.CLILifecycleSharedPool, api.Shared, nil},
		{"bogus", api.PerUser, nil},
	} {
		d := base()
		d.Workload.Lifecycle = tc.lifecycle
		d.Workload.Class = tc.class
		if tc.lifecycle == api.CLILifecycleToolbox {
			d.Workload.Toolbox = "user-box"
		}
		if tc.extra != nil {
			tc.extra(&d)
		}
		if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
			t.Fatalf("lifecycle=%s class=%s admitted: %v", tc.lifecycle, tc.class, err)
		}
	}
	// Toolbox id without the toolbox lifecycle denies.
	d = base()
	d.Workload.Toolbox = "user-box"
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("toolbox id without lifecycle: %v", err)
	}
	// Workspace scope/access validate.
	d = base()
	d.Workload.Lifecycle = api.CLILifecycleBinding
	d.Workload.WorkspaceScope = "bogus"
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("bad workspace scope: %v", err)
	}
	d = base()
	d.Workload.Lifecycle = api.CLILifecycleBinding
	d.Workload.WorkspaceAccess = "bogus"
	if err := d.Validate(); !errors.Is(err, api.ErrInvalid) {
		t.Fatalf("bad workspace access: %v", err)
	}
	// A well-formed toolbox definition validates.
	d = base()
	d.Workload.Lifecycle = api.CLILifecycleToolbox
	d.Workload.Toolbox = "user-box"
	if err := d.Validate(); err != nil {
		t.Fatalf("valid toolbox definition denied: %v", err)
	}
	// A well-formed shared-pool definition validates.
	d = base()
	d.Workload.Class = api.Shared
	d.Workload.Lifecycle = api.CLILifecycleSharedPool
	d.Workload.Stateless = true
	d.Workload.WorkspaceScope = "none"
	d.Credentials = nil
	if err := d.Validate(); err != nil {
		t.Fatalf("valid pool definition denied: %v", err)
	}
}

func TestLoadRejectsInvalidSnapshotRecords(t *testing.T) {
	for _, doc := range []string{
		`{"schema":1,"workloads":[{"workload_id":"x"}]}`,
		`{"schema":1,"shared_credential_policies":[{"policy_id":"x"}]}`,
		`{"schema":1,"credential_references":[{"credential_ref_id":"x"}]}`,
		`{"schema":1,"definitions":[{"definition_id":"x"}]}`,
		`{"schema":1,"capability_changes":[{}]}`,
		`{"schema":1,"capability_changes":[{"grant":{},"policy":{}}]}`,
	} {
		path := filepath.Join(t.TempDir(), "registry.json")
		if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("invalid snapshot admitted: %s", doc)
		}
	}
}
