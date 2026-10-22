package api

import (
	"testing"
)

// A proxy grant must carry everything the egress sidecar needs to inject the
// real header: the resolved value plus the declared prefix. The workload env
// still keeps only the stub — the secret travels the plan channel, never the
// container surface.
func TestProxyStubPlanCarriesValueAndPrefix(t *testing.T) {
	effective := EffectiveBinding{
		Definition: ToolDefinition{
			Credentials: []CredentialInput{
				{Name: "ACME_TOKEN", Required: true, Delivery: "proxy", Target: "api.acme.example", Prefix: "Bearer "},
				{Name: "PLAIN", Delivery: "env"},
			},
		},
		Credential: &CredentialReference{CredentialRefID: "cred_1"},
	}
	environment := map[string]string{"ACME_TOKEN": "real-secret", "PLAIN": "visible"}
	plan := ProxyStubPlan(effective, environment)
	if environment["ACME_TOKEN"] != ProxyStubValue {
		t.Fatalf("env stub = %q", environment["ACME_TOKEN"])
	}
	if len(plan) != 1 {
		t.Fatalf("grants=%+v", plan)
	}
	grant := plan[0]
	if grant.Value != "real-secret" || grant.Prefix != "Bearer " || grant.EnvName != "ACME_TOKEN" || grant.Reference != "cred_1" {
		t.Fatalf("grant=%+v", grant)
	}
}

// The controller plan must carry the grants the injector produced so the
// admission service can wire the cred-proxy sidecar. Grants are runtime-only:
// they never enter the persisted binding.
func TestControllerPlanForCarriesProxyGrants(t *testing.T) {
	effective := EffectiveBinding{
		WorkloadID: "w-1",
		Definition: ToolDefinition{DefinitionID: "d", Version: "1.0.0"},
		ProxyGrants: []ProxyGrant{
			{EnvName: "ACME_TOKEN", Reference: "cred_1", Hosts: []string{"api.acme.example"}, Prefix: "Bearer ", Value: "real-secret"},
		},
	}
	plan, err := ControllerPlanFor(effective)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ProxyGrants) != 1 || plan.ProxyGrants[0].Value != "real-secret" || plan.ProxyGrants[0].Hosts[0] != "api.acme.example" {
		t.Fatalf("plan grants=%+v", plan.ProxyGrants)
	}
}
