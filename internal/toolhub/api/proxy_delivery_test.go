package api

import (
	"errors"
	"testing"
)

// M13 P6 S6.3: proxy delivery admits only a host inside the declared egress
// allowlist — the egress-proxy plan may inject the real credential nowhere
// else. The workload env then carries the "hub-managed" stub.
func TestProxyDeliveryValidation(t *testing.T) {
	base := func(mut func(*ToolDefinition)) ToolDefinition {
		d := ToolDefinition{
			Schema:       SchemaVersion,
			DefinitionID: "acme-api",
			Version:      "1.0.0",
			Transport:    RemoteMCP,
			Source:       DefinitionSource{URL: "https://mcp.acme.example", TLSMode: "required"},
			Tools:        []ToolSpec{{Name: "acme-list", Effect: ReadEffect}},
			Workload:     WorkloadPolicy{Class: PerUser, Rationale: "tenant API"},
			Execution:    ExecutionPolicy{TimeoutSeconds: 10, OutputBytes: 4096, CPUMillis: 250, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"api.acme.example"}},
			Health:       HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
			Credentials:  []CredentialInput{{Name: "ACME_API_KEY", Required: true, Delivery: "proxy", Target: "api.acme.example"}},
		}
		if mut != nil {
			mut(&d)
		}
		return d
	}
	if err := base(nil).Validate(); err != nil {
		t.Fatalf("proxy delivery inside the egress allowlist rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ToolDefinition){
		"target-outside-egress": func(d *ToolDefinition) { d.Credentials[0].Target = "evil.example" },
		"target-empty":          func(d *ToolDefinition) { d.Credentials[0].Target = "" },
		"target-not-a-host":     func(d *ToolDefinition) { d.Credentials[0].Target = "api.acme.example/x" },
		"prefix-crlf":           func(d *ToolDefinition) { d.Credentials[0].Prefix = "Bearer\r\nInjected: x" },
	} {
		d := base(mutate)
		if err := d.Validate(); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrUnsupportedCredentialContract) {
			t.Fatalf("%s: proxy delivery accepted an invalid target/prefix: %v", name, err)
		}
	}
	// A recipe may declare the same proxy delivery for a connection field.
	recipe := ConnectionRecipe{Fields: []ConnectionField{{Name: "ACME_API_KEY", Type: "token", Required: true, Secret: true, Delivery: "proxy", Target: "api.acme.example"}}}
	if err := recipe.Validate(); err != nil {
		t.Fatalf("recipe proxy delivery rejected: %v", err)
	}
}
