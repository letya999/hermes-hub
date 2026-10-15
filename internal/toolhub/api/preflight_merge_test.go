package api

import (
	"errors"
	"fmt"
	"testing"
)

func TestCredentialGateFromAuthErrors(t *testing.T) {
	if _, _, ok := CredentialGateFrom(ImportedArtifact{}, nil); ok {
		t.Fatal("nil error gated")
	}
	if _, _, ok := CredentialGateFrom(ImportedArtifact{}, errors.New("connection refused")); ok {
		t.Fatal("non-auth error gated")
	}
	imported := ImportedArtifact{Definition: ToolDefinition{
		Tools:  []ToolSpec{{Name: "x"}},
		Source: DefinitionSource{ToolContractDigest: "sha256:abc", ToolContractSource: "x"},
	}}
	gated, gate, ok := CredentialGateFrom(imported, errors.New("MCP exited: set the SLACK_TOKEN environment variable"))
	if !ok || gate == nil {
		t.Fatal("auth error not gated")
	}
	if len(gate.Names) != 1 || gate.Names[0] != "SLACK_TOKEN" {
		t.Fatalf("gate names: %v", gate.Names)
	}
	if len(gated.Definition.Tools) != 0 || gated.Definition.Source.ToolContractDigest != "" {
		t.Fatal("placeholder contract survived gating")
	}
	if !errors.Is(gate, ErrIsolation) {
		t.Fatal("gate does not unwrap to ErrIsolation")
	}
	imported2 := ImportedArtifact{Definition: ToolDefinition{
		Credentials: []CredentialInput{
			{Name: "JIRA_TOKEN", Required: true}, {Name: "JIRA_SECRET", Required: true},
			{Name: "CONFLUENCE_TOKEN", Required: true}, {Name: "CONFLUENCE_SECRET", Required: true},
			{Name: "OPTIONAL_FLAG"},
		},
	}}
	gated2, gate2, ok := CredentialGateFrom(imported2, fmt.Errorf("probe failed: %w", ErrEmptyToolList))
	if !ok || len(gate2.Names) != 4 {
		t.Fatalf("empty-tools gate: %v %v", ok, gate2)
	}
	if len(gated2.Definition.CredentialGroups) != 2 {
		t.Fatalf("prefix alternative groups: %v", gated2.Definition.CredentialGroups)
	}
	single := ImportedArtifact{Definition: ToolDefinition{
		Credentials: []CredentialInput{{Name: "API_TOKEN", Required: true}},
	}}
	gated3, _, ok := CredentialGateFrom(single, fmt.Errorf("x: %w", ErrEmptyToolList))
	if !ok || len(gated3.Definition.CredentialGroups) != 0 {
		t.Fatalf("single prefix grouped: %v", gated3.Definition.CredentialGroups)
	}
}

func TestCredentialAlternativeGroups(t *testing.T) {
	err := errors.New("authentication required: either FOO_TOKEN or BAR_TOKEN, or both BAZ_TOKEN and QUX_TOKEN must be provided")
	gated, gate, ok := CredentialGateFrom(ImportedArtifact{}, err)
	if !ok {
		t.Fatal("alternative auth error not gated")
	}
	if len(gate.Names) != 4 {
		t.Fatalf("names: %v", gate.Names)
	}
	found := false
	for _, group := range gated.Definition.CredentialGroups {
		if len(group) == 2 && group[0] == "BAZ_TOKEN" && group[1] == "QUX_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("paired group missing: %v", gated.Definition.CredentialGroups)
	}
	plain := CredentialGate{Names: []string{"A_TOKEN"}}
	if plain.Error() == "" {
		t.Fatal("gate error text empty")
	}
	var nilGate *CredentialGate
	if nilGate.Error() == "" {
		t.Fatal("nil gate error text empty")
	}
}
