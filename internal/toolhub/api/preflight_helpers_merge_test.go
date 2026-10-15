package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestPreflightProbeHelpers(t *testing.T) {
	help := "Usage: server\n\nEnvironment Variables:\n  API_TOKEN   required: auth token\n  REGION      optional region\n  OTHER_KEY   recommended\n\nMore text\n"
	creds := ParseMCPHelpCredentials(help)
	if len(creds) != 2 || creds[0].Name != "API_TOKEN" || creds[1].Name != "OTHER_KEY" || !creds[0].Required {
		t.Fatalf("help credentials: %+v", creds)
	}
	if ParseMCPHelpCredentials("no environment section") != nil {
		t.Fatal("unmarked help produced credentials")
	}

	// fallback only fires when the artifact declares a network transport
	imported := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Args: []string{"--transport", "sse"}}}}
	calls := 0
	oldNet := NetworkMCPToolList
	defer func() { NetworkMCPToolList = oldNet }()
	NetworkMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		calls++
		return []ToolSpec{{Name: "t", Effect: ReadEffect}}, nil
	}
	tools, out, _, err := NetworkTransportFallback(context.Background(), imported, errors.New("stdio failed"), nil)
	if err != nil || calls != 1 || len(tools) != 1 || out.Definition.Source.NetworkTransport != "sse" {
		t.Fatalf("fallback: %v %v %v", tools, out.Definition.Source.NetworkTransport, err)
	}
	// no declared transport → first error is returned untouched
	if _, _, _, err := NetworkTransportFallback(context.Background(), ImportedArtifact{}, errors.New("x"), nil); err == nil || calls != 1 {
		t.Fatal("stdio artifact probed over network")
	}
	NetworkMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, errors.New("net too") }
	if _, _, _, err := NetworkTransportFallback(context.Background(), imported, errors.New("stdio"), nil); err == nil || !strings.Contains(err.Error(), "network transport probe") {
		t.Fatalf("combined error: %v", err)
	}

	// credential retry promotes named or declared-optional secrets
	imp2 := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{
		{Name: "API_TOKEN"}, {Name: "REGION"},
	}}}
	list := func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "ok", Effect: ReadEffect}}, nil
	}
	promoted, retry, err := RetryListWithPromotedCredentials(context.Background(), imp2, errors.New("crash"), list)
	if err != nil || len(retry) != 1 {
		t.Fatalf("retry: %v", err)
	}
	if !promoted.Definition.Credentials[0].Required || promoted.Definition.Credentials[1].Required {
		t.Fatalf("promotion: %+v", promoted.Definition.Credentials)
	}
	// nothing to promote → hard error
	if _, _, err := RetryListWithPromotedCredentials(context.Background(), ImportedArtifact{}, errors.New("x"), list); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no creds: %v", err)
	}

	// restamp applies the confirmed list to the definition
	def := ToolDefinition{Schema: SchemaVersion, DefinitionID: "d1", Version: "1.0.0"}
	restamped, contract, err := RestampFromMCPList(context.Background(), ImportedArtifact{Definition: def},
		func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
			return []ToolSpec{{Name: "a", Effect: ReadEffect}}, nil
		})
	if err != nil || len(restamped.Definition.Tools) != 1 || len(contract.Tools) != 1 || contract.Source == "" {
		t.Fatalf("restamp: %v %+v", err, contract)
	}
	if _, _, err := RestampFromMCPList(context.Background(), imported, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil list: %v", err)
	}
	// pull validation runs before docker
	if err := PullPublishedImage(context.Background(), "bad name", "sha256:"+fmt.Sprint("x")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("pull name: %v", err)
	}
}
