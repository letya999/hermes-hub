package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The MCP stdio decoder skips server startup logs and non-response JSON,
// enforces the JSON-RPC error envelope, names the tools/list reply, and
// bounds schemas and descriptions.
func TestDecodeMCPToolListBranches(t *testing.T) {
	stream := strings.Join([]string{
		"server starting on port 0",
		`{"level":"info","msg":"boot"}`,
		`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress"}`,
		`{not json`,
		`{"jsonrpc":"2.0","id":2,"result":{"tools":[` +
			`{"name":"fs.read","description":"read a file","inputSchema":{"type":"object"},` +
			`"annotations":{"readOnlyHint":true}},` +
			`{"name":"fs.write","annotations":{"readOnlyHint":true,"destructiveHint":true}}]}}`,
	}, "\n")
	tools, err := DecodeMCPToolList(strings.NewReader(stream))
	if err != nil || len(tools) != 2 {
		t.Fatalf("decode: %v %v", tools, err)
	}
	if tools[0].Effect != ReadEffect || tools[1].Effect != WriteEffect {
		t.Fatalf("effects: %+v", tools)
	}
	if _, err := DecodeMCPToolList(strings.NewReader(`{"id":2,"error":{"message":"boom"}}`)); err == nil {
		t.Fatal("rpc error envelope accepted")
	}
	if _, err := DecodeMCPToolList(strings.NewReader(`{"id":2,"result":{"tools":[{"name":"bad name!"}]}}`)); err == nil {
		t.Fatal("invalid tool name accepted")
	}
	if _, err := DecodeMCPToolList(strings.NewReader(`{"id":2,"result":{"tools":[]}}`)); err == nil {
		t.Fatal("empty tools/list accepted")
	}
	big := fmt.Sprintf(`{"id":2,"result":{"tools":[{"name":"x","inputSchema":"%s"}]}}`, strings.Repeat("a", 70000))
	if _, err := DecodeMCPToolList(strings.NewReader(big)); err == nil {
		t.Fatal("oversized input schema accepted")
	}
	if _, err := DecodeMCPToolList(strings.NewReader("")); err == nil {
		t.Fatal("empty stream accepted")
	}
}

// The help-text credential parser only collects required/recommended names
// inside the Environment Variables section.
func TestParseMCPHelpCredentials(t *testing.T) {
	help := strings.Join([]string{
		"Usage: server [flags]",
		"Environment Variables:",
		"  FOO_TOKEN   API token (required)",
		"  BAR_URL     endpoint url",
		"  BAZ_SECRET  signing secret, recommended",
		"",
		"  AFTER       ignored",
	}, "\n")
	got := ParseMCPHelpCredentials(help)
	if len(got) != 2 || got[0].Name != "FOO_TOKEN" || !got[0].Required || got[1].Name != "BAZ_SECRET" {
		t.Fatalf("parsed credentials: %+v", got)
	}
	if got := ParseMCPHelpCredentials("no environment section"); got != nil {
		t.Fatalf("unexpected credentials: %+v", got)
	}
}

// Credential-promotion retry renames only secret-shaped optional inputs named
// by the auth failure, and the network-transport fallback prefers the real
// transport probe once stdio fails.
func TestPreflightRetryAndFallback(t *testing.T) {
	ctx := context.Background()
	imported := ImportedArtifact{}
	imported.Definition.Credentials = []CredentialInput{
		{Name: "FOO_TOKEN"},
		{Name: "PLAIN_SETTING"},
	}
	gateErr := errors.New("server exited: Either FOO_TOKEN must be provided")
	okList := func(context.Context, ImportedArtifact) ([]ToolSpec, error) {
		return []ToolSpec{{Name: "fs.read", Effect: ReadEffect}}, nil
	}
	promoted, tools, err := RetryListWithPromotedCredentials(ctx, imported, gateErr, okList)
	if err != nil || len(tools) != 1 {
		t.Fatalf("retry: %v %v", tools, err)
	}
	for _, c := range promoted.Definition.Credentials {
		if c.Name == "FOO_TOKEN" && !c.Required {
			t.Fatal("named credential not promoted")
		}
		if c.Name == "PLAIN_SETTING" && c.Required {
			t.Fatal("non-secret input promoted")
		}
	}
	if _, _, err := RetryListWithPromotedCredentials(ctx, ImportedArtifact{}, errors.New("crash"), okList); err == nil {
		t.Fatal("retry without declared credentials accepted")
	}
	failList := func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, errors.New("still gated") }
	if _, _, err := RetryListWithPromotedCredentials(ctx, imported, gateErr, failList); err == nil {
		t.Fatal("failed retry accepted")
	}

	// Network transport fallback: no declared transport or a nil error passes
	// the original failure through untouched.
	same := func(context.Context, ImportedArtifact) ([]ToolSpec, error) { return nil, nil }
	if _, _, fn, err := NetworkTransportFallback(ctx, ImportedArtifact{}, nil, same); fn == nil || err != nil {
		t.Fatal("fallback rewrote a clean probe")
	}
	netImported := ImportedArtifact{}
	netImported.Definition.Source.Args = []string{"--transport", "http"}
	defer func(orig func(context.Context, ImportedArtifact) ([]ToolSpec, error)) { NetworkMCPToolList = orig }(NetworkMCPToolList)
	NetworkMCPToolList = okList
	tools, stamped, fn, err := NetworkTransportFallback(ctx, netImported, errors.New("stdio failed"), same)
	if err != nil || len(tools) != 1 || stamped.Definition.Source.NetworkTransport != "streamable-http" {
		t.Fatalf("network fallback: %v %v %q", tools, err, stamped.Definition.Source.NetworkTransport)
	}
	_ = fn
	NetworkMCPToolList = failList
	if _, _, _, err := NetworkTransportFallback(ctx, netImported, errors.New("stdio failed"), same); err == nil || !strings.Contains(err.Error(), "network transport probe") {
		t.Fatalf("combined error missing: %v", err)
	}
}

// Small probe helpers: bounded text, random names, file-mode copy, and the
// image-pull name gate that rejects before docker runs.
func TestPreflightProbeHelpers(t *testing.T) {
	if got := boundedProbeText("a\n\n b\tc " + strings.Repeat("x", 1600)); len(got) > 1500 || strings.Contains(got, "\n") {
		t.Fatalf("boundedProbeText: %d %q", len(got), got[:30])
	}
	name, err := RandomPreflightContainerName("hermes-preflight-")
	if err != nil || !strings.HasPrefix(name, "hermes-preflight-") || len(name) != len("hermes-preflight-")+8 {
		t.Fatalf("container name: %q %v", name, err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in")
	dst := filepath.Join(dir, "out")
	if err := os.WriteFile(src, []byte("bin"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CopyFileMode(src, dst, 0755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(dst)
	if err != nil || string(body) != "bin" {
		t.Fatalf("copied content: %q %v", body, err)
	}
	if err := CopyFileMode(filepath.Join(dir, "missing"), dst, 0755); err == nil {
		t.Fatal("missing source copied")
	}
	if err := PullPublishedImage(context.Background(), "bad image!", "sha256:abc"); err == nil {
		t.Fatal("bad image pull accepted")
	}
	reapPreflightContainer("not-a-preflight-name") // early return, no docker
}
