package api

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func readyDefinition(digest string) ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "def-1", Version: "1.0.0",
		Transport: ContainerMCP,
		Source:    DefinitionSource{Image: "img", Digest: digest, Repository: "https://github.com/acme/demo", CommitSHA: strings.Repeat("0", 40)},
		Tools:     []ToolSpec{{Name: "t", Effect: ReadEffect}},
		Workload:  WorkloadPolicy{Class: PerUser, Rationale: "owner-scoped test tool", ToolHiveVersion: "v0.48.0", SidecarImages: []string{RestrictedBuildProxyImage}},
		Execution: ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 1024, CPUMillis: 100, MemoryMiB: 64, MaxPIDs: 4, Egress: []string{"api.example.io"}},
		Health:    HealthProbe{Kind: "exec", Value: "probe", TimeoutSeconds: 5},
	}
}

func TestEnsureReadyContainerContract(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	def := readyDefinition(digest)
	eff := EffectiveBinding{WorkloadID: "wl-1", Definition: def,
		Binding: ToolBinding{PrincipalID: "p1", ContextID: "ctx-1", ToolBindingID: "bind-1"}}

	// non-container transports are already ready
	agentEff := eff
	agentEff.Definition = ToolDefinition{Transport: AgentTools}
	if err := (MCPBackend{}).EnsureReady(t.Context(), agentEff, nil); err != nil {
		t.Fatalf("agent ready: %v", err)
	}
	// a verified running receipt admits the workload
	backend := MCPBackend{AdmissionVerifier: func(_ context.Context, e EffectiveBinding) (AdmissionReceipt, error) {
		if !reflect.DeepEqual(e.Definition.Execution, def.Execution) {
			t.Fatalf("plan drift: %+v", e.Definition.Execution)
		}
		return AdmissionReceipt{WorkloadID: "wl-1", State: "running", Enforced: true, ImageDigest: digest, Execution: def.Execution, SidecarImages: def.Workload.SidecarImages}, nil
	}}
	if err := backend.EnsureReady(t.Context(), eff, nil); err != nil {
		t.Fatalf("ready: %v", err)
	}
	// controller failure surfaces as isolation
	backend.AdmissionVerifier = func(context.Context, EffectiveBinding) (AdmissionReceipt, error) {
		return AdmissionReceipt{}, errors.New("docker down")
	}
	if err := backend.EnsureReady(t.Context(), eff, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("admit failure: %v", err)
	}
	// a receipt that does not match the pinned plan is rejected
	backend.AdmissionVerifier = func(context.Context, EffectiveBinding) (AdmissionReceipt, error) {
		return AdmissionReceipt{WorkloadID: "wl-1", State: "running", Enforced: true, ImageDigest: "sha256:" + strings.Repeat("ff", 32), Execution: def.Execution}, nil
	}
	if err := backend.EnsureReady(t.Context(), eff, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("receipt drift: %v", err)
	}
	// file-credential workloads need an admission release hook
	mounted := eff
	mounted.CredentialMounts = []Mount{{Source: "connection-state", Target: "/state"}}
	if err := (MCPBackend{}).EnsureReady(t.Context(), mounted, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("mount without release: %v", err)
	}
}
