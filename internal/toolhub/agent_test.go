package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func agentDefinition() ToolDefinition {
	return ToolDefinition{
		Schema:       SchemaVersion,
		DefinitionID: "hub-agent-tools",
		Version:      "1.0.0",
		Transport:    AgentTools,
		Source:       DefinitionSource{},
		Workload:     WorkloadPolicy{Class: PerUser, Rationale: "in-process executor inside the owning runtime"},
		Execution:    ExecutionPolicy{TimeoutSeconds: 120, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"api.hh.ru", "communication-hub"}},
		Health:       HealthProbe{Kind: "exec", Value: "hubctl", TimeoutSeconds: 5},
		Tools: []ToolSpec{
			{Name: "file_read", Effect: ReadEffect, CapabilityID: "files", Uses: []CapabilityUse{{Action: "read", Resource: "files", PathArgument: "path"}},
				Arguments: []CLIArgument{{Name: "root", Type: "string"}, {Name: "path", Type: "string", Required: true}}},
			{Name: "service_catalog", Effect: ReadEffect, CapabilityID: "services", Uses: []CapabilityUse{{Action: "read", Resource: "services"}}},
		},
	}
}

func TestAgentDefinitionContract(t *testing.T) {
	definition := agentDefinition()
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	definition.Source = DefinitionSource{Command: "hubctl"}
	if err := definition.Validate(); err == nil {
		t.Fatal("agent-tools accepted a remote source")
	}
	definition = agentDefinition()
	definition.Tools[0].Arguments[0].Flag = "--root"
	if err := definition.Validate(); err == nil {
		t.Fatal("agent-tools accepted a CLI flag on a typed argument")
	}
}

func TestAgentBackendFailClosedAndRouting(t *testing.T) {
	e := EffectiveBinding{Definition: agentDefinition()}
	if _, err := (RoutingBackend{}).CallEnv(context.Background(), e, e.Definition.Tools[0], map[string]any{"path": "a"}, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing agent backend must fail closed: %v", err)
	}
	if _, err := (AgentExecBackend{}).CallEnv(context.Background(), e, e.Definition.Tools[0], nil, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing exec channel must fail closed: %v", err)
	}
	wrong := e
	wrong.Definition.Transport = RemoteMCP
	if _, err := (AgentExecBackend{Exec: func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{}, nil
	}}).CallEnv(context.Background(), wrong, e.Definition.Tools[0], nil, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong transport must be rejected: %v", err)
	}
}

func TestAgentExecCarriesAdmittedScopes(t *testing.T) {
	var got AgentExecRequest
	backend := RoutingBackend{Agent: AgentExecBackend{Exec: func(_ context.Context, e EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
		got = request
		return AgentExecResult{Result: map[string]any{"text": "content"}}, nil
	}}}
	e := EffectiveBinding{Definition: agentDefinition(),
		CapabilityScopes: []CapabilityScope{{Resource: "files", PathArgument: "path", PathPrefix: "docs"}}}
	result, err := backend.CallEnv(context.Background(), e, e.Definition.Tools[0], map[string]any{"path": "docs/a.txt"}, nil)
	if err != nil || result.Structured == nil {
		t.Fatalf("dispatch failed: %v %+v", err, result)
	}
	if got.Tool != "file_read" || len(got.Scopes) != 1 || got.Scopes[0].PathPrefix != "docs" || got.Arguments["path"] != "docs/a.txt" {
		t.Fatalf("executor request lost admission scope: %+v", got)
	}
}

func TestAgentExecToolError(t *testing.T) {
	backend := AgentExecBackend{Exec: func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{Error: "capability scope denied"}, nil
	}}
	e := EffectiveBinding{Definition: agentDefinition()}
	result, err := backend.CallEnv(context.Background(), e, e.Definition.Tools[0], nil, nil)
	if err != nil || !result.IsError {
		t.Fatalf("tool-level failure must surface as IsError: %v %+v", err, result)
	}
	backend = AgentExecBackend{Exec: func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
		return AgentExecResult{}, errors.New("channel dead")
	}}
	if _, err := backend.CallEnv(context.Background(), e, e.Definition.Tools[0], nil, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("channel failure must be an isolation error: %v", err)
	}
}

func TestManagedAgentContainerName(t *testing.T) {
	binding := EffectiveBinding{Binding: ToolBinding{PrincipalID: "alice", ContextID: "alice"}}
	name, err := ManagedAgentContainer(binding)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("alice\x00alice\x00gateway"))
	want := "hermes-context-" + hex.EncodeToString(sum[:8])
	if name != want {
		t.Fatalf("container derivation diverged from supervisor contract: %s != %s", name, want)
	}
	if _, err := ManagedAgentContainer(EffectiveBinding{}); err == nil {
		t.Fatal("empty binding resolved a container")
	}
}

// A conversion reads its source AND creates a new artifact: per IS-03 the
// destination needs its own create authority, so a convert-only rule set
// must deny dispatch entirely.
func TestAgentConvertNeedsCreateAuthority(t *testing.T) {
	s := NewStore()
	definition := agentDefinition()
	definition.Tools = append(definition.Tools, ToolSpec{Name: "document_convert", Effect: WriteEffect, CapabilityID: "documents",
		Uses:      []CapabilityUse{{Action: "convert", Resource: "documents", PathArgument: "path"}, {Action: "create", Resource: "artifacts"}},
		Arguments: []CLIArgument{{Name: "path", Type: "string", Required: true}, {Name: "name", Type: "string", Required: true}, {Name: "format", Type: "string", Required: true}}})
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
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
	digest := DefinitionDigest(definition)
	convert := CapabilityRule{CapabilityID: "documents", ImplementationDigest: digest, Action: "convert", Resource: "documents", PathPrefix: "docs", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
	create := CapabilityRule{CapabilityID: "documents", ImplementationDigest: digest, Action: "create", Resource: "artifacts", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
	policy := CapabilityPolicy{Schema: SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "test", Status: ActiveStatus, Ceiling: []CapabilityRule{convert, create}, Defaults: []CapabilityRule{convert}}
	profile := CapabilityProfile{Schema: SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "test", Status: ActiveStatus,
		Selections: []CapabilitySelection{{CapabilityID: "documents", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest,
			ToolName: "document_convert", Name: "doc_convert"}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	calls := 0
	g := &Gateway{Store: s, AuditWrite: func(string, map[string]string) error { return nil },
		Injector: func(context.Context, EffectiveBinding) (CredentialInjection, error) {
			return CredentialInjection{}, nil
		},
		Backend: RoutingBackend{Agent: AgentExecBackend{Exec: func(context.Context, EffectiveBinding, AgentExecRequest) (AgentExecResult, error) {
			calls++
			return AgentExecResult{Result: map[string]any{"path": "docs/artifacts/documents/out.html"}}, nil
		}}}}
	args := map[string]any{"path": "docs/notes.md", "name": "out", "format": "html"}
	if _, err := g.CallAuthorized(t.Context(), auth, "doc_convert", args); err == nil || calls != 0 {
		t.Fatalf("convert dispatched without artifact create authority: %v", err)
	}
	// Grant the destination create authority through a reviewed policy
	// revision; the same call then reaches the executor.
	policy.Revision++
	policy.Defaults = append(policy.Defaults, create)
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	profile.Revision++
	profile.PolicyRevision = policy.Revision
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	if _, err := g.CallAuthorized(t.Context(), auth, "doc_convert", args); err != nil || calls != 1 {
		t.Fatalf("convert denied with both authorities: %v calls=%d", err, calls)
	}
}

// Full admission path: policy + profile select the agent definition with a
// path prefix; the executor receives the verbatim scope the policy bound.
func TestAgentToolsEndToEndScopes(t *testing.T) {
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
	digest := DefinitionDigest(definition)
	rule := CapabilityRule{CapabilityID: "files", ImplementationDigest: digest, Action: "read", Resource: "files", PathPrefix: "docs", Limits: CapabilityLimits{OutputBytes: 8192, TimeoutSeconds: 30}}
	policy := CapabilityPolicy{Schema: SchemaVersion, PolicyID: "org-default", Organization: "example", Members: []string{"alice"}, Revision: 1,
		IssuedBy: "operator", IssuedAt: time.Now().UTC(), Reason: "test", Status: ActiveStatus, Ceiling: []CapabilityRule{rule}, Defaults: []CapabilityRule{rule}}
	profile := CapabilityProfile{Schema: SchemaVersion, ProfileID: auth.CapabilityProfile, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID,
		Environment: auth.Environment, Generation: auth.Generation, PolicyVersion: auth.PolicyVersion, PolicyID: policy.PolicyID, PolicyRevision: policy.Revision,
		Revision: 1, IssuedBy: "operator", IssuedAt: policy.IssuedAt, Reason: "test", Status: ActiveStatus,
		Selections: []CapabilitySelection{{CapabilityID: "files", DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ImplementationDigest: digest,
			ToolName: "file_read", Name: "workspace_read"}}}
	if err := putPolicy(t, s, policy); err != nil {
		t.Fatal(err)
	}
	if err := putProfile(t, s, profile); err != nil {
		t.Fatal(err)
	}
	var exec AgentExecRequest
	g := &Gateway{Store: s, AuditWrite: func(string, map[string]string) error { return nil },
		Injector: func(context.Context, EffectiveBinding) (CredentialInjection, error) {
			return CredentialInjection{}, nil
		},
		Backend: RoutingBackend{Agent: AgentExecBackend{Exec: func(_ context.Context, e EffectiveBinding, request AgentExecRequest) (AgentExecResult, error) {
			exec = request
			return AgentExecResult{Result: map[string]any{"text": "file body"}}, nil
		}}}}
	out, err := g.CallAuthorized(t.Context(), auth, "workspace_read", map[string]any{"path": "docs/readme.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if exec.Tool != "file_read" || len(exec.Scopes) != 1 || exec.Scopes[0].PathPrefix != "docs" || exec.Scopes[0].Resource != "files" || exec.Scopes[0].PathArgument != "path" {
		t.Fatalf("executor got wrong scope: %+v", exec)
	}
	if out == nil || out.StructuredContent == nil {
		t.Fatal("structured result lost")
	}
	// The projected description names the admitted boundary, not the catalog.
	projected, err := s.ListProjectedTools(auth)
	if err != nil || len(projected) != 1 {
		t.Fatalf("projection: %v %v", len(projected), err)
	}
	if !strings.Contains(projected[0].Tool.Description, "files under docs") {
		t.Fatalf("description does not name the admitted scope: %q", projected[0].Tool.Description)
	}
	// Raw names and unselected tools never reach the executor.
	for _, denied := range []string{"file_read", "service_catalog", "hub-agent-tools"} {
		if _, err := g.CallAuthorized(t.Context(), auth, denied, map[string]any{"path": "docs/readme.txt"}); err == nil {
			t.Fatalf("unprojected name %s reached the executor", denied)
		}
	}
	// A path outside the admitted prefix is denied before the executor runs.
	if _, err := g.CallAuthorized(t.Context(), auth, "workspace_read", map[string]any{"path": "private/readme.txt"}); err == nil {
		t.Fatal("out-of-scope path reached the executor")
	}
}
