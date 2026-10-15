package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NetworkTransportFallback retries a failed stdio probe over the declared
// network transport and stamps the definition on success; without a declared
// transport or a first error it is a passthrough.
func TestNetworkTransportFallbackContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	list := func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, nil }
	passthrough := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Args: []string{"serve"}}}}
	tools, got, gotList, err := NetworkTransportFallback(ctx, passthrough, errors.New("stdio failed"), list)
	if err == nil || gotList == nil || len(tools) != 0 {
		t.Fatal("no declared transport must pass through unchanged")
	}
	got.Definition.Source.NetworkTransport = "sse"
	_, again, _, err := NetworkTransportFallback(ctx, got, nil, list)
	if err != nil || again.Definition.Source.NetworkTransport != "sse" {
		t.Fatalf("nil firstErr must be passthrough: %v", err)
	}

	orig := NetworkMCPToolList
	defer func() { NetworkMCPToolList = orig }()
	declared := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Args: []string{"--transport", "sse"}}}}
	NetworkMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "net_tool", Effect: ReadEffect, InputSchema: []byte(`{"type":"object"}`)}}, nil
	}
	tools, stamped, netList, err := NetworkTransportFallback(ctx, declared, errors.New("stdio failed"), list)
	if err != nil || stamped.Definition.Source.NetworkTransport != "sse" || len(tools) != 1 || netList == nil {
		t.Fatalf("network fallback must stamp transport and return tools: %v %+v", err, stamped.Definition.Source)
	}
	NetworkMCPToolList = func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("listener refused")
	}
	_, _, _, err = NetworkTransportFallback(ctx, declared, errors.New("stdio failed"), list)
	if err == nil || !errors.Is(err, err) {
		t.Fatal("failed network probe must surface a combined error")
	}
	if want := "stdio failed"; err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "network transport probe") {
		t.Fatalf("combined error must keep both probes: %v", err)
	}
}

// RetryListWithPromotedCredentials promotes exactly the credentials a failed
// probe named, adds undeclared ones, and refuses a retry when nothing in the
// contract can change.
func TestRetryListWithPromotedCredentials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fail := func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, errors.New("boom") }
	base := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{
		{Name: "FOO_TOKEN"},
		{Name: "PLAIN_URL"},
	}}}

	// A succeeding list returns the promoted contract and tools.
	ok := func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "t1", Effect: ReadEffect, InputSchema: []byte(`{"type":"object"}`)}}, nil
	}
	promoted, tools, err := RetryListWithPromotedCredentials(ctx, base, errors.New("set BAR_SECRET and FOO_TOKEN is required"), ok)
	if err != nil || len(tools) != 1 {
		t.Fatalf("promoted retry must succeed: %v", err)
	}
	byName := map[string]CredentialInput{}
	for _, input := range promoted.Definition.Credentials {
		byName[input.Name] = input
	}
	if !byName["FOO_TOKEN"].Required || !byName["BAR_SECRET"].Required {
		t.Fatalf("named credentials must be promoted/required: %+v", promoted.Definition.Credentials)
	}
	if byName["PLAIN_URL"].Required {
		t.Fatal("non-secret optional input must stay optional")
	}

	// Error names nothing but optional secrets exist: all are promoted.
	all := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "SOME_TOKEN"}, {Name: "OTHER_KEY", Required: true}}}}
	got, _, err := RetryListWithPromotedCredentials(ctx, all, errors.New("probe died"), ok)
	if err != nil || !got.Definition.Credentials[0].Required || !got.Definition.Credentials[1].Required {
		t.Fatalf("fallback must promote optional secrets: %v %+v", err, got.Definition.Credentials)
	}

	// Nothing promotable: refuse the retry.
	plain := ImportedArtifact{Definition: ToolDefinition{Credentials: []CredentialInput{{Name: "PLAIN_URL"}}}}
	if _, _, err := RetryListWithPromotedCredentials(ctx, plain, errors.New("probe died"), ok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no change must refuse: %v", err)
	}
	// A list failure propagates.
	if _, _, err := RetryListWithPromotedCredentials(ctx, all, errors.New("probe died"), fail); err == nil || err.Error() != "boom" {
		t.Fatalf("list error must propagate: %v", err)
	}
}

// ParseMCPHelpCredentials keeps only env vars described as required or
// recommended inside the "Environment Variables:" section.
func TestParseMCPHelpCredentials(t *testing.T) {
	t.Parallel()
	help := `Usage: server [options]

Environment Variables:
  FOO_TOKEN        required for auth
  BAR_URL          optional endpoint
  BAZ_KEY          recommended for setup
lowername        required but lowercase is not an env var name
  not-a-var        malformed line

Other Section:
  QUX_SECRET       required but outside the section
`
	got := ParseMCPHelpCredentials(help)
	names := map[string]bool{}
	for _, input := range got {
		names[input.Name] = true
		if !input.Required {
			t.Fatalf("only required inputs must be returned: %+v", input)
		}
	}
	if !names["FOO_TOKEN"] || !names["BAZ_KEY"] || names["BAR_URL"] || names["QUX_SECRET"] {
		t.Fatalf("unexpected credential set: %v", names)
	}
	if got := ParseMCPHelpCredentials("no env section"); len(got) != 0 {
		t.Fatalf("no section must yield nothing: %v", got)
	}
}

// Small preflight helpers: name generation, probe-text bounding, file mode
// copy, the container-name guard, and Docker-entry validation branches.
func TestPreflightDockerEntryGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	name, err := RandomPreflightContainerName("hermes-preflight-")
	if err != nil || len(name) != len("hermes-preflight-")+8 {
		t.Fatalf("name: %q %v", name, err)
	}
	reapPreflightContainer("foreign-container") // must return without touching docker

	if got := boundedProbeText("a  b\n c" + strings.Repeat(" x", 2000)); len(got) > 1500 || strings.Contains(got, "\n") {
		t.Fatalf("bounded text: %d", len(got))
	}
	dir := t.TempDir()
	if err := CopyFileMode(filepath.Join(dir, "src"), filepath.Join(dir, "dst"), 0600); err == nil {
		t.Fatal("missing source must error")
	}
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("bin"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileMode(src, filepath.Join(dir, "dst"), 0755); err != nil {
		t.Fatal(err)
	}

	// Validation branches that must reject before any docker invocation.
	if err := PullPublishedImage(ctx, "../evil", "sha256:zz"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid published ref: %v", err)
	}
	badImage := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Image: "../evil"}}}
	if _, err := ListMCPToolsFromLocalImage(ctx, badImage); !errors.Is(err, ErrInvalid) && err == nil {
		t.Fatalf("invalid local image: %v", err)
	}
	if _, err := NetworkProbeToolList(ctx, badImage); !errors.Is(err, ErrInvalid) && err == nil {
		t.Fatalf("invalid network image: %v", err)
	}
	noTransport := ImportedArtifact{Definition: ToolDefinition{Source: DefinitionSource{Image: "repo/ok:1"}}}
	if _, err := NetworkProbeToolList(ctx, noTransport); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing transport must be ErrInvalid: %v", err)
	}
	if _, _, err := RestampFromMCPList(ctx, ImportedArtifact{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil list func: %v", err)
	}
	if _, _, err := RestampFromMCPList(ctx, ImportedArtifact{}, func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return nil, errors.New("list failed")
	}); err == nil || err.Error() != "list failed" {
		t.Fatalf("list error must propagate: %v", err)
	}
}
