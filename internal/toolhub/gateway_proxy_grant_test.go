package toolhub

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// The injection's proxy grants must reach the backend's effective binding so
// the admission layer can forward them into the controller plan — otherwise
// the cred-proxy sidecar is never wired and the workload keeps a dead stub.
type proxyGrantCaptureBackend struct {
	effective api.EffectiveBinding
}

func (b *proxyGrantCaptureBackend) Call(ctx context.Context, effective api.EffectiveBinding, tool api.ToolSpec, arguments map[string]any) (api.BackendResult, error) {
	return b.CallEnv(ctx, effective, tool, arguments, nil)
}

func (b *proxyGrantCaptureBackend) CallEnv(_ context.Context, effective api.EffectiveBinding, _ api.ToolSpec, _ map[string]any, _ map[string]string) (api.BackendResult, error) {
	b.effective = effective
	return api.BackendResult{}, nil
}

func TestGatewayPropagatesProxyGrants(t *testing.T) {
	s := NewStore()
	definition := agentDefinition()
	if err := s.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.TelegramEnvelope("alice", 7, "runtime", "policy-1")
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, PolicyVersion: auth.PolicyVersion, WorkloadClass: api.PerUser, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, "", "")
	if err := s.PutBinding(binding); err != nil {
		t.Fatal(err)
	}
	backend := &proxyGrantCaptureBackend{}
	g := &Gateway{Store: s, AuditWrite: func(string, map[string]string) error { return nil },
		Injector: func(context.Context, api.EffectiveBinding) (api.CredentialInjection, error) {
			return api.CredentialInjection{
				Environment: map[string]string{"ACME_TOKEN": api.ProxyStubValue},
				Proxy:       []api.ProxyGrant{{EnvName: "ACME_TOKEN", Reference: "cred_1", Hosts: []string{"api.acme.example"}, Prefix: "Bearer ", Value: "real-secret"}},
			}, nil
		},
		Backend: backend,
	}
	name := api.ProjectedToolName(definition.DefinitionID, definition.Version, "service_catalog")
	if _, err := g.CallAuthorized(t.Context(), auth, name, nil); err != nil {
		t.Fatal(err)
	}
	if len(backend.effective.ProxyGrants) != 1 || backend.effective.ProxyGrants[0].Value != "real-secret" {
		t.Fatalf("backend saw grants=%+v", backend.effective.ProxyGrants)
	}
}
