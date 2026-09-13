package toolhub

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

import "github.com/letya999/hermes-hub/internal/identity"

func remoteDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "google-work", Version: "1.0.0", Transport: RemoteMCP,
		Source:      DefinitionSource{URL: "https://api.example.com/mcp", TLSMode: "required"},
		Tools:       []ToolSpec{{Name: "search", Effect: ReadEffect}, {Name: "send", Effect: WriteEffect}},
		Credentials: []CredentialInput{{Name: "GOOGLE_TOKEN", Required: true}},
		Workload:    WorkloadPolicy{Class: PerUser, Rationale: "OAuth account and provider session are user-owned"},
		Execution:   ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.example.com"}},
		Health:      HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
}

func statefulContainerDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "stateful-mcp", Version: "2.1.0", Transport: ContainerMCP,
		Source: DefinitionSource{
			Image:   "ghcr.io/example/stateful-mcp",
			Digest:  "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Command: "/app/server",
		},
		Tools:       []ToolSpec{{Name: "read", Effect: ReadEffect}},
		Credentials: []CredentialInput{{Name: "SERVICE_TOKEN", Required: true}},
		Workload:    WorkloadPolicy{Class: PerUser, Stateful: true, Rationale: "Provider session and durable connector state are user-owned", ToolHiveVersion: "v0.48.0", SidecarImages: []string{"ghcr.io/stacklok/toolhive/egress-proxy@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}},
		Execution: ExecutionPolicy{
			TimeoutSeconds: 60, OutputBytes: 2 << 20, CPUMillis: 1000, MemoryMiB: 512, MaxPIDs: 64,
			Egress: []string{"service.example.com"},
			Mounts: []Mount{{Source: "connection-state", Target: "/state", ReadOnly: false}},
		},
		Health: HealthProbe{Kind: "exec", Value: "/app/health", TimeoutSeconds: 5},
	}
}

func boundedCLIDefinition() ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "gitlab-cli", Version: "1.0.0", Transport: BoundedCLI,
		Source:      DefinitionSource{Command: "glab", Args: []string{"mr", "list", "--output", "json"}},
		Credentials: []CredentialInput{{Name: "GITLAB_TOKEN", Required: true}},
		Workload:    WorkloadPolicy{Class: PerJob, Rationale: "CLI process is bounded and cleaned after one job"},
		Execution:   ExecutionPolicy{TimeoutSeconds: 45, OutputBytes: 512 << 10, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"gitlab.com"}},
		Health:      HealthProbe{Kind: "exec", Value: "glab version", TimeoutSeconds: 5},
		Tools:       []ToolSpec{{Name: "list", Effect: ReadEffect}},
	}
}

func seededStore(t *testing.T) (*Store, identity.Envelope, ToolBinding) {
	t.Helper()
	store := NewStore()
	definition := remoteDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	credential := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	if err := store.PutCredentialReference(credential); err != nil {
		t.Fatal(err)
	}
	connection := Connection{Schema: SchemaVersion, ConnectionID: "google-work", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: definition.DefinitionID, CredentialRefID: credential.CredentialRefID, Revision: 1, Status: ActiveStatus, Metadata: map[string]string{"account": "alice@example.com"}}
	if err := store.PutConnection(connection); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	if err := store.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	return store, auth, binding
}

func TestDefinitionValidationExamplesAndStrictDecode(t *testing.T) {
	for _, definition := range []ToolDefinition{remoteDefinition(), statefulContainerDefinition(), boundedCLIDefinition()} {
		if err := definition.Validate(); err != nil {
			t.Fatalf("%s: %v", definition.DefinitionID, err)
		}
	}
	projected := ProjectedToolName("google-work", "1.0.0", "search")
	if projected != ProjectedToolName("google-work", "1.0.0", "search") || projected == ProjectedToolName("google-work", "2.0.0", "search") {
		t.Fatal("projection naming is not deterministic and versioned")
	}
	if _, err := DecodeDefinition([]byte(`{"schema":1,"definition_id":"x","version":"1.0.0","transport":"remote-mcp","source":{"url":"https://x.example/mcp","tls_mode":"required"},"tools":[{"name":"read","effect":"read"}],"workload":{"class":"shared","rationale":"stateless"},"execution":{"timeout_seconds":1,"output_bytes":1,"cpu_millis":1,"memory_mib":16,"max_pids":1,"egress":["x.example"]},"health":{"kind":"http","value":"/health","timeout_seconds":1},"unknown":true}`)); err == nil {
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
	bad.Execution.TimeoutSeconds = MaxExecutionTimeout + 1
	if err := bad.Validate(); err == nil {
		t.Fatal("unbounded CLI accepted")
	}
	bad = remoteDefinition()
	bad.Workload.Class = Shared
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

func TestBoundedCLISchemaCoversScalarArgumentTypes(t *testing.T) {
	for _, kind := range []string{"string", "integer", "number", "boolean"} {
		definition := boundedCLIDefinition()
		definition.Tools[0].Arguments = []CLIArgument{{Name: "value", Flag: "--value", Type: kind}}
		if err := definition.Validate(); err != nil {
			t.Fatalf("valid CLI type %q rejected: %v", kind, err)
		}
	}
	for _, argument := range []CLIArgument{{Name: "value", Flag: "value", Type: "string"}, {Name: "value", Flag: "--value", Type: "object"}} {
		definition := boundedCLIDefinition()
		definition.Tools[0].Arguments = []CLIArgument{argument}
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
	if _, err := store.Resolve(bob, binding.ToolBindingID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-owner resolve error=%v", err)
	}
	stalePolicy := auth
	stalePolicy.PolicyVersion = "policy-2"
	if _, err := store.Resolve(stalePolicy, binding.ToolBindingID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("stale policy error=%v", err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/2", []string{"GOOGLE_TOKEN"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrStale) {
		t.Fatalf("rotated credential error=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("disabled binding error=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked binding error=%v", err)
	}
}

func TestConcurrentRevocationCannotPassAdmission(t *testing.T) {
	store, auth, binding := seededStore(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	authorized := make(chan error, 1)
	go func() {
		authorized <- store.Authorize(auth, binding.ToolBindingID, func(EffectiveBinding) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- store.SetBindingStatus(binding.ToolBindingID, RevokedStatus) }()
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
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrRevoked) {
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
	if _, err := store.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrStale) {
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
	if _, err := loaded.Resolve(auth, binding.ToolBindingID); !errors.Is(err, ErrStale) {
		t.Fatalf("round-tripped stale binding error=%v", err)
	}
}

func TestWorkloadClassLifecycleModel(t *testing.T) {
	store, _, binding := seededStore(t)
	owner := &OwnerRef{Type: ContextOwner, ID: "alice"}
	workload, err := NewWorkloadInstance(binding, owner, "", 1, time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	jobDefinition := boundedCLIDefinition()
	jobDefinition.Credentials = nil
	jobBinding := binding
	jobBinding.WorkloadClass = PerJob
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
	jobBinding.ToolBindingID = DeterministicBindingID(jobBinding.PrincipalID, jobBinding.ContextID, jobBinding.RuntimeID, jobBinding.DefinitionID, jobBinding.DefinitionVersion, "", "")
	job, err := NewWorkloadInstance(jobBinding, owner, "job-1", 1, time.Now(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(job); err != nil {
		t.Fatal(err)
	}
}

func TestStrictValidationMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ToolDefinition)
	}{
		{"schema", func(d *ToolDefinition) { d.Schema = 2 }},
		{"id", func(d *ToolDefinition) { d.DefinitionID = "Bad" }},
		{"version", func(d *ToolDefinition) { d.Version = "latest" }},
		{"class", func(d *ToolDefinition) { d.Workload.Class = "unknown" }},
		{"rationale", func(d *ToolDefinition) { d.Workload.Rationale = " " }},
		{"no tools", func(d *ToolDefinition) { d.Tools = nil }},
		{"tool name", func(d *ToolDefinition) { d.Tools[0].Name = "Bad Name" }},
		{"tool effect", func(d *ToolDefinition) { d.Tools[0].Effect = "delete" }},
		{"duplicate tool", func(d *ToolDefinition) { d.Tools = append(d.Tools, d.Tools[0]) }},
		{"credential name", func(d *ToolDefinition) { d.Credentials[0].Name = "token" }},
		{"duplicate credential", func(d *ToolDefinition) { d.Credentials = append(d.Credentials, d.Credentials[0]) }},
		{"remote TLS", func(d *ToolDefinition) { d.Source.TLSMode = "optional" }},
		{"remote credentials in URL", func(d *ToolDefinition) {
			d.Source.URL = (&url.URL{Scheme: "https", Host: "api.example.com", Path: "/mcp", User: url.UserPassword("user", "pass")}).String()
		}},
		{"remote query", func(d *ToolDefinition) { d.Source.URL = "https://api.example.com/mcp?x=1" }},
		{"remote health kind", func(d *ToolDefinition) { d.Health.Kind = "exec" }},
		{"remote health path", func(d *ToolDefinition) { d.Health.Value = "health" }},
		{"container tag", func(d *ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Source.Image = "ghcr.io/example/mcp:latest"
		}},
		{"container command", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Source.Command = "sh -c" }},
		{"container argument secret", func(d *ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Source.Args = []string{"${SERVICE_TOKEN}"}
		}},
		{"container health kind", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Health.Kind = "http" }},
		{"cli command", func(d *ToolDefinition) { *d = boundedCLIDefinition(); d.Source.Command = "sh -c" }},
		{"cli source URL", func(d *ToolDefinition) { *d = boundedCLIDefinition(); d.Source.URL = "https://gitlab.com" }},
		{"shared state", func(d *ToolDefinition) {
			d.Workload.Class = Shared
			d.Credentials[0].PerRequest = true
			d.Workload.Stateful = true
		}},
		{"shared static credential", func(d *ToolDefinition) { d.Workload.Class = Shared }},
		{"per-job state no mount", func(d *ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Workload.Class = PerJob
			d.Execution.Mounts = nil
		}},
		{"bad timeout", func(d *ToolDefinition) { d.Execution.TimeoutSeconds = MaxExecutionTimeout + 1 }},
		{"bad output", func(d *ToolDefinition) { d.Execution.OutputBytes = MaxOutputBytes + 1 }},
		{"bad cpu", func(d *ToolDefinition) { d.Execution.CPUMillis = MaxCPUMillis + 1 }},
		{"bad memory", func(d *ToolDefinition) { d.Execution.MemoryMiB = MaxMemoryMiB + 1 }},
		{"bad pids", func(d *ToolDefinition) { d.Execution.MaxPIDs = MaxPIDs + 1 }},
		{"no egress", func(d *ToolDefinition) { d.Execution.Egress = nil }},
		{"bad egress", func(d *ToolDefinition) { d.Execution.Egress = []string{"https://api.example.com"} }},
		{"duplicate egress", func(d *ToolDefinition) { d.Execution.Egress = []string{"api.example.com", "api.example.com"} }},
		{"unsafe mount", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].Target = "state" }},
		{"host mount", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].Source = "host" }},
		{"readonly connection state", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].ReadOnly = true }},
		{"wrong workspace mount", func(d *ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Execution.Mounts[0] = Mount{Source: "workspace-readonly", Target: "/workspace", ReadOnly: false}
		}},
		{"wrong job mount", func(d *ToolDefinition) {
			*d = remoteDefinition()
			d.Execution.Mounts = []Mount{{Source: "job-state", Target: "/state", ReadOnly: false}}
		}},
		{"duplicate mount target", func(d *ToolDefinition) {
			*d = statefulContainerDefinition()
			d.Execution.Mounts = append(d.Execution.Mounts, Mount{Source: "connection-state", Target: "/state"})
		}},
		{"empty mount source", func(d *ToolDefinition) { *d = statefulContainerDefinition(); d.Execution.Mounts[0].Source = "" }},
		{"health timeout", func(d *ToolDefinition) { d.Health.TimeoutSeconds = 31 }},
		{"health empty", func(d *ToolDefinition) { d.Health.Value = "" }},
		{"health newline", func(d *ToolDefinition) { d.Health.Value = "/health\n" }},
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
	if err := (ExecutionPolicy{TimeoutSeconds: 1, OutputBytes: 1, CPUMillis: 1, MemoryMiB: 16, MaxPIDs: 1, Egress: []string{"x.example"}, Mounts: []Mount{{Source: "x", Target: "/x"}}}).validate(Shared); err == nil {
		t.Fatal("shared host mount accepted")
	}
}

func TestRecordValidationAndStoreConflicts(t *testing.T) {
	store, _, binding := seededStore(t)
	if err := (OwnerRef{Type: "other", ID: "alice"}).Validate(); err == nil {
		t.Fatal("invalid owner accepted")
	}
	if (OwnerRef{Type: "other", ID: "alice"}).Matches(identity.TelegramEnvelope("alice", 1, "runtime", "policy-1")) {
		t.Fatal("unknown owner matched")
	}
	definition := remoteDefinition()
	definition.Tools[0].Name = "different"
	if err := store.RegisterDefinition(definition); !errors.Is(err, ErrConflict) {
		t.Fatalf("definition conflict=%v", err)
	}
	if err := store.RegisterDefinition(remoteDefinition()); err != nil {
		t.Fatal(err)
	}
	credential := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/changed", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	if err := store.PutCredentialReference(credential); !errors.Is(err, ErrConflict) {
		t.Fatalf("credential conflict=%v", err)
	}
	badConnection := Connection{Schema: SchemaVersion, ConnectionID: "other-connection", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: "missing", Revision: 1, Status: ActiveStatus}
	if err := store.PutConnection(badConnection); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connection definition=%v", err)
	}
	badConnection.DefinitionID = "google-work"
	badConnection.CredentialRefID = "missing-credential"
	if err := store.PutConnection(badConnection); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing connection credential=%v", err)
	}
	if err := store.SetConnectionStatus("missing", DisabledStatus); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connection status=%v", err)
	}
	if err := store.SetConnectionStatus("google-work", "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad connection status=%v", err)
	}
	if err := store.SetConnectionStatus("google-work", RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionStatus("google-work", ActiveStatus); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked connection re-enabled=%v", err)
	}
	if _, err := store.RotateCredential("google-work", "local", "local://alice/google/next", []string{"GOOGLE_TOKEN"}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked rotation=%v", err)
	}
	if err := store.SetBindingStatus("missing", DisabledStatus); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing binding status=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad binding status=%v", err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, ActiveStatus); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked binding re-enabled=%v", err)
	}
	if err := store.Authorize(identity.TelegramEnvelope("alice", 1, "runtime", "policy-1"), binding.ToolBindingID, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil authorize callback=%v", err)
	}
}

func TestDecodeLoadAndDenyPaths(t *testing.T) {
	definitionBytes, err := json.Marshal(remoteDefinition())
	if err != nil {
		t.Fatal(err)
	}
	if definition, err := DecodeDefinition(definitionBytes); err != nil || definition.DefinitionID != "google-work" {
		t.Fatalf("valid definition decode=%+v err=%v", definition, err)
	}
	for _, body := range [][]byte{[]byte("{"), append(append([]byte(nil), definitionBytes...), []byte("{}")...)} {
		if _, err := DecodeDefinition(body); err == nil {
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
	if _, err := store.Resolve(invalidAuth, binding.ToolBindingID); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid auth=%v", err)
	}
	if _, err := store.Resolve(auth, "bad id"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid binding id=%v", err)
	}
	if _, err := store.Resolve(auth, "missing-binding"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing binding=%v", err)
	}
	if _, err := Load(filepath.Join("relative", "toolhub.json")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative store path=%v", err)
	}
	symlinkTarget := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(symlinkTarget, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(symlinkTarget, symlinkPath); err == nil {
		if _, err := Load(symlinkPath); !errors.Is(err, ErrInvalid) {
			t.Fatalf("symlink store path=%v", err)
		}
	}
	if err := store.Authorize(auth, binding.ToolBindingID, func(EffectiveBinding) error { return errors.New("backend failed") }); err == nil || err.Error() != "backend failed" {
		t.Fatalf("admission result=%v", err)
	}
}

func TestRecordAndWorkloadDenyPaths(t *testing.T) {
	store, _, binding := seededStore(t)
	credential := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID("google-work", 1), ConnectionID: "google-work", Revision: 1, Backend: "local", Locator: "local://alice/google/1", Keys: []string{"GOOGLE_TOKEN"}, Status: ActiveStatus}
	credentialCases := []func(*CredentialReference){
		func(r *CredentialReference) { r.Schema = 2 },
		func(r *CredentialReference) { r.CredentialRefID = "bad id" },
		func(r *CredentialReference) { r.ConnectionID = "bad id" },
		func(r *CredentialReference) { r.Revision = 0 },
		func(r *CredentialReference) { r.Backend = "" },
		func(r *CredentialReference) { r.Locator = "secret=value" },
		func(r *CredentialReference) { r.Keys = nil },
		func(r *CredentialReference) { r.Keys = []string{"bad-key"} },
		func(r *CredentialReference) { r.Status = DisabledStatus },
	}
	for _, mutate := range credentialCases {
		candidate := credential
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid credential reference accepted")
		}
	}
	connection := Connection{Schema: SchemaVersion, ConnectionID: "google-work", Owner: OwnerRef{Type: PrincipalOwner, ID: "alice"}, DefinitionID: "google-work", Revision: 1, Status: ActiveStatus}
	connectionCases := []func(*Connection){
		func(c *Connection) { c.Schema = 2 },
		func(c *Connection) { c.ConnectionID = "bad id" },
		func(c *Connection) { c.DefinitionID = "bad id" },
		func(c *Connection) { c.Revision = 0 },
		func(c *Connection) { c.Status = "unknown" },
		func(c *Connection) { c.Owner = OwnerRef{} },
		func(c *Connection) { c.CredentialRefID = "bad id" },
		func(c *Connection) { c.Metadata = map[string]string{"secret": "no"} },
	}
	for _, mutate := range connectionCases {
		candidate := connection
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid connection accepted")
		}
	}
	if err := store.PutBinding(ToolBinding{Schema: SchemaVersion, PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", DefinitionID: "missing", DefinitionVersion: "1.0.0", PolicyVersion: "policy-1", WorkloadClass: PerUser, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing binding definition=%v", err)
	}
	badBinding := binding
	badBinding.ToolBindingID = "manual-binding"
	if err := store.PutBinding(badBinding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nondeterministic binding=%v", err)
	}
	badBinding = binding
	badBinding.ToolBindingID = ""
	badBinding.ConnectionRevision++
	if err := store.PutBinding(badBinding); !errors.Is(err, ErrStale) {
		t.Fatalf("stale binding revision=%v", err)
	}
	badBinding = binding
	badBinding.ToolBindingID = ""
	badBinding.CredentialRefID = ""
	if err := store.PutBinding(badBinding); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing binding credential=%v", err)
	}
	if err := store.PutWorkloadInstance(WorkloadInstance{Schema: SchemaVersion, WorkloadID: "work-missing", BindingID: "missing", DefinitionID: "google-work", DefinitionVersion: "1.0.0", Class: PerUser, Owner: &OwnerRef{Type: ContextOwner, ID: "alice"}, RuntimeID: "runtime", Generation: 1, Status: StartingStatus}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing workload binding=%v", err)
	}
	workload, err := NewWorkloadInstance(binding, &OwnerRef{Type: ContextOwner, ID: "alice"}, "", 1, time.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutWorkloadInstance(workload); err != nil {
		t.Fatal(err)
	}
	workload.Status = FailedStatus
	if err := store.PutWorkloadInstance(workload); !errors.Is(err, ErrConflict) {
		t.Fatalf("mutable workload accepted=%v", err)
	}
	invalidWorkload := workload
	invalidWorkload.Schema = 2
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("invalid workload accepted")
	}
	invalidWorkload = workload
	invalidWorkload.Class = Shared
	invalidWorkload.Owner = &OwnerRef{Type: ContextOwner, ID: "alice"}
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("owned shared workload accepted")
	}
	invalidWorkload = workload
	invalidWorkload.Class = PerJob
	invalidWorkload.JobID = "job-1"
	invalidWorkload.ExpiresAt = time.Time{}
	if err := invalidWorkload.Validate(); err == nil {
		t.Fatal("unbounded per-job workload accepted")
	}
}
