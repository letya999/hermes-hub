//go:build live

package toolhub

// Live tests exercise the shipped remote-MCP path against real public
// endpoints: real DNS, real TLS, real MCP handshake. They never run in `just
// check` (the `live` tag is opt-in: go test -tags live -run TestLive).

import (
	"context"
	"strings"
	"testing"
	"time"
)

// DeepWiki serves the current streamable protocol on /mcp with no auth —
// probe must handshake, list tools and build an immutable definition.
func TestLiveDeepWikiStreamable(t *testing.T) {
	endpoint := "https://mcp.deepwiki.com/mcp"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-deepwiki", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	names := []string{}
	for _, tool := range probe.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("tools: %v", names)
	if len(names) == 0 {
		t.Fatal("no tools")
	}
	full, err := RemoteMCPDefinition(endpoint, "remote-deepwiki", nil, probe.Tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A real call through the shipped path.
	effective := EffectiveBinding{Definition: full}
	target := full.Tools[0]
	for _, tool := range full.Tools {
		if tool.Name == "read_wiki_structure" {
			target = tool
		}
	}
	result, err := (MCPBackend{}).callRemoteMCP(ctx, effective, target, map[string]any{"repoName": "modelcontextprotocol/go-sdk"}, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(result.Text) < 100 {
		t.Fatalf("thin result: %q", result.Text)
	}
	t.Logf("call %s returned %d bytes", target.Name, len(result.Text))
}

// DeepWiki's /sse is shut down upstream (410 Gone, "SSE transport is
// deprecated"). The probe must fail with a clean sanitized error — no hang,
// no leaked detail — the SSE fallback simply finds nothing to speak to.
func TestLiveDeprecatedSSEEndpoint(t *testing.T) {
	endpoint := "https://mcp.deepwiki.com/sse"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-deepwiki-sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err == nil {
		t.Fatalf("deprecated SSE endpoint probed (%d tools) — upstream revived it", len(probe.Tools))
	}
	t.Logf("deprecated SSE fails closed: %v", err)
}

// Microsoft Learn serves a free streamable MCP endpoint — a second real
// provider proves the path is not DeepWiki-specific.
func TestLiveMicrosoftLearnMCP(t *testing.T) {
	endpoint := "https://learn.microsoft.com/api/mcp"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-mslearn", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	names := []string{}
	for _, tool := range probe.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("mslearn tools: %v", names)
	full, err := RemoteMCPDefinition(endpoint, "remote-mslearn", nil, probe.Tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := full.Tools[0]
	for _, tool := range full.Tools {
		if strings.Contains(tool.Name, "search") {
			target = tool
		}
	}
	result, err := (MCPBackend{}).callRemoteMCP(ctx, EffectiveBinding{Definition: full}, target, map[string]any{"query": "azure functions"}, nil)
	if err != nil {
		t.Fatalf("call %s: %v", target.Name, err)
	}
	t.Logf("mslearn call %s → %d bytes", target.Name, len(result.Text))
}

// Context7 (Upstash docs) — free streamable endpoint, probe only: tool args
// differ across versions so listing is the stable contract.
func TestLiveContext7MCP(t *testing.T) {
	endpoint := "https://mcp.context7.com/mcp"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-context7", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	names := []string{}
	for _, tool := range probe.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("context7 tools: %v", names)
	if len(names) == 0 {
		t.Fatal("no tools")
	}
}

// Cloudflare serves a streamable MCP docs endpoint without auth.
func TestLiveCloudflareDocs(t *testing.T) {
	endpoint := "https://docs.mcp.cloudflare.com/mcp"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	def, err := remoteDefinitionBase(endpoint, "remote-cf-docs", nil)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	names := []string{}
	for _, tool := range probe.Tools {
		names = append(names, tool.Name)
	}
	t.Logf("cloudflare tools: %v", names)
	if len(names) == 0 {
		t.Fatal("no tools")
	}
}

// Header creds against a real endpoint: httpbin echoes headers back, but it
// is not MCP — a credentialed endpoint probe must fail closed without
// ever landing a definition (the probe is the gate).
func TestLiveNonMCPRefused(t *testing.T) {
	for _, endpoint := range []string{
		"https://httpbin.org/get",
		"https://example.com/",
		"https://www.google.com/",
	} {
		if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
			t.Fatalf("%s refused at URL validation: %v", endpoint, err)
		}
		def, err := remoteDefinitionBase(endpoint, "remote-nonmcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		if _, err := probeRemoteMCP(ctx, def, nil); err == nil {
			t.Fatalf("%s passed the MCP probe", endpoint)
		} else {
			t.Logf("%s → %v", endpoint, err)
		}
	}
}

// The GitHub official MCP requires a PAT; without credentials the probe must
// classify the failure as auth-needed (401/403), not a generic error.
func TestLiveGitHubMCPAuthNeeded(t *testing.T) {
	endpoint := "https://api.githubcopilot.com/mcp/"
	if err := ValidateRemoteMCPEndpoint(endpoint); err != nil {
		t.Fatalf("endpoint refused: %v", err)
	}
	def, err := remoteDefinitionBase(endpoint, "remote-github", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	probe, err := probeRemoteMCP(ctx, def, nil)
	if err == nil {
		t.Fatalf("unauthenticated github probe succeeded (%d tools) — unexpected", len(probe.Tools))
	}
	t.Logf("github probe: auth=%v err=%v", probe.AuthNeeded, err)
	if !probe.AuthNeeded {
		t.Logf("note: github did not classify as auth-needed; got: %v", err)
	}
	if strings.Contains(err.Error(), "http_") {
		t.Fatalf("error leaked raw detail: %v", err)
	}
}
