package api

import (
	"context"
	"errors"
	"testing"
)

// Remote MCP paths re-validate the endpoint at call/readiness time and report
// sanitized isolation errors, never the raw endpoint or dial details.
func TestRemoteBackendGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := MCPBackend{}
	bad := EffectiveBinding{Definition: ToolDefinition{DefinitionID: "remote-bad", Transport: RemoteMCP, Source: DefinitionSource{URL: "http://127.0.0.1:9/x"}}}
	if _, err := backend.CallRemoteMCP(ctx, bad, ToolSpec{Name: "x"}, nil, nil); err == nil {
		t.Fatal("private endpoint must be refused")
	}
	if err := backend.remoteReady(ctx, bad, nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("readiness failure must wrap ErrIsolation: %v", err)
	}
	if _, err := ProbeRemoteMCP(ctx, ToolDefinition{Source: DefinitionSource{URL: "https://"}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed endpoint: %v", err)
	}
}
