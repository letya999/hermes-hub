package api

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestPreflightURLPlaceholder(t *testing.T) {
	packet := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "METABASE_URL", Required: true}, {Name: "METABASE_API_KEY", Required: true}, {Name: "OPTIONAL_URL"}}}}
	args, cleanup, err := PreflightCredentialArgs(context.Background(), packet)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !slices.Contains(args, "METABASE_URL=https://preflight.invalid") || !slices.Contains(args, "METABASE_API_KEY=preflight") || slices.Contains(args, "OPTIONAL_URL=https://preflight.invalid") {
		t.Fatalf("wrong configuration placeholders: %v", args)
	}
	run := PreflightDockerRunArgs(packet, "probe", nil, nil, args...)
	i := slices.Index(run, "--network")
	if i < 0 || run[i+1] != "none" {
		t.Fatal("placeholder probe gained network access")
	}
}

func TestMetabaseConfigurationGate(t *testing.T) {
	message := "Either (METABASE_URL and METABASE_API_KEY) or (METABASE_URL, METABASE_USERNAME, and METABASE_PASSWORD) environment variables are required"
	message += "\nserver stderr: " + message
	packet := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "METABASE_URL"}}, Tools: []ToolSpec{{Name: "unverified"}}}}
	got, gate, ok := CredentialGateFrom(packet, errors.New(message))
	if !ok || gate == nil {
		t.Fatal("missing configuration must reach the protected form")
	}
	want := [][]string{{"METABASE_URL", "METABASE_API_KEY"}, {"METABASE_URL", "METABASE_USERNAME", "METABASE_PASSWORD"}}
	if !reflect.DeepEqual(got.Definition.CredentialGroups, want) {
		t.Fatalf("complete alternative auth groups: got %v want %v", got.Definition.CredentialGroups, want)
	}
	for _, name := range []string{"METABASE_URL", "METABASE_API_KEY", "METABASE_USERNAME", "METABASE_PASSWORD"} {
		if !slices.ContainsFunc(got.Definition.Credentials, func(in CredentialInput) bool { return in.Name == name && in.Required }) {
			t.Fatalf("required input missing: %s", name)
		}
	}
	if len(got.Definition.Tools) != 0 || got.Definition.Source.ToolContractDigest != "" {
		t.Fatal("credential gate must not admit unverified tools")
	}
	for _, message := range []string{"segfault METABASE_URL", "Usage: METABASE_API_KEY", "MODULE_PATH and CACHE_DIR environment variables are required"} {
		if _, _, ok := CredentialGateFrom(packet, errors.New(message)); ok {
			t.Fatalf("non-connection failure opened a credential gate: %s", message)
		}
	}
}

func TestCredentialRetryRetainsCompleteAlternatives(t *testing.T) {
	packet := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "METABASE_API_KEY", Required: true}, {Name: "METABASE_URL"}}}}
	message := errors.New("Either (METABASE_URL and METABASE_API_KEY) or (METABASE_URL, METABASE_USERNAME, and METABASE_PASSWORD) environment variables are required")
	got, tools, err := RetryListWithPromotedCredentials(t.Context(), packet, message, func(_ context.Context, p ImportedArtifact) ([]ToolSpec, error) {
		if !slices.ContainsFunc(p.Definition.Credentials, func(in CredentialInput) bool { return in.Name == "METABASE_URL" && in.Required }) {
			t.Fatal("retry omitted required URL")
		}
		return []ToolSpec{{Name: "read", Effect: ReadEffect}}, nil
	})
	want := [][]string{{"METABASE_URL", "METABASE_API_KEY"}, {"METABASE_URL", "METABASE_USERNAME", "METABASE_PASSWORD"}}
	if err != nil || len(tools) != 1 || !reflect.DeepEqual(got.Definition.CredentialGroups, want) {
		t.Fatalf("retry lost alternative credential contract: %+v %v", got.Definition, err)
	}
}
