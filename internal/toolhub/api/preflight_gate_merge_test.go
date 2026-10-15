package api

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestCredentialGateFromContract(t *testing.T) {
	if _, _, ok := CredentialGateFrom(ImportedArtifact{}, nil); ok {
		t.Fatal("nil error gated")
	}
	imported := ImportedArtifact{Definition: ToolDefinition{
		DefinitionID: "d1", Credentials: []CredentialInput{{Name: "REGION"}},
		Tools:  []ToolSpec{{Name: "placeholder"}},
		Source: DefinitionSource{ToolContractDigest: "sha256:x", ToolContractSource: "preflight-list"},
	}}
	// auth failure text naming secret-shaped env vars opens the gate
	err := fmt.Errorf("exit: either SLACK_TOKEN or both JIRA_TOKEN and JIRA_SECRET is required")
	gated, gate, ok := CredentialGateFrom(imported, err)
	if !ok || gate == nil {
		t.Fatal("auth error not gated")
	}
	if !slices.Contains(gate.Names, "SLACK_TOKEN") {
		t.Fatalf("names: %v", gate.Names)
	}
	// placeholder tools and the unconfirmed contract digest are dropped
	if gated.Definition.Tools != nil || gated.Definition.Source.ToolContractDigest != "" {
		t.Fatal("placeholder contract survived the gate")
	}
	// named credentials become required inputs on the definition
	found := false
	for _, in := range gated.Definition.Credentials {
		if in.Name == "SLACK_TOKEN" {
			found = in.Required
		}
	}
	if !found {
		t.Fatalf("credentials: %+v", gated.Definition.Credentials)
	}
	// a plain crash with no credential names is not a gate
	if _, _, ok := CredentialGateFrom(imported, errors.New("segfault")); ok {
		t.Fatal("crash gated")
	}
	// answered-empty tools/list promotes declared required inputs
	declared := ImportedArtifact{Definition: ToolDefinition{
		Credentials: []CredentialInput{{Name: "JIRA_TOKEN", Required: true}, {Name: "JIRA_URL", Required: true}, {Name: "OPTIONAL", Required: false}},
	}}
	gated, gate, ok = CredentialGateFrom(declared, ErrEmptyToolList)
	if !ok || !slices.Equal(gate.Names, []string{"JIRA_TOKEN", "JIRA_URL"}) {
		t.Fatalf("empty list gate: %v %v", gate, ok)
	}
	if len(gated.Definition.CredentialGroups) != 0 {
		t.Fatalf("groups: %v", gated.Definition.CredentialGroups)
	}
	// no required inputs + empty list = real failure, no gate
	if _, _, ok := CredentialGateFrom(ImportedArtifact{}, ErrEmptyToolList); ok {
		t.Fatal("empty list with no credentials gated")
	}
	var gateErr *CredentialGate
	if !errors.As(&CredentialGate{}, &gateErr) {
		t.Fatal("nil gate Error must not panic")
	}
	if (&CredentialGate{}).Unwrap() != ErrIsolation {
		t.Fatal("gate must unwrap to isolation")
	}
}

func TestAdmitWithSubmittedCredentials(t *testing.T) {
	def := ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "sub-1", Version: "1.0.0", Transport: ContainerMCP,
		Source: DefinitionSource{Image: "img", Digest: "sha256:" + "ab", Command: "run"},
	}
	old := LocalMCPToolList
	defer func() { LocalMCPToolList = old }()
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("spawn failed")
	}
	if _, err := AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), def, map[string]string{"API_TOKEN": "x"}); err == nil {
		t.Fatal("probe failure admitted")
	}
	LocalMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "ping", Effect: ReadEffect}}, nil
	}
	got, err := AdmitWithSubmittedCredentials(context.Background(), t.TempDir(), def, map[string]string{"API_TOKEN": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "ping" {
		t.Fatalf("tools: %+v", got.Tools)
	}
}
