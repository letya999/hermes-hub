package main

import (
	"context"
	"flag"

	"github.com/letya999/hermes-hub/services/harness-runtime/internal/agenttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tools is the in-cell MCP stdio executor: the same agent-tools surface the
// daemon serves, over one stdio session. It never runs on the control plane.
func runTools(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("tools", flag.ContinueOnError)
	organizationRoot := f.String("organization", "", "mounted organization docs root for tools")
	workspace := f.String("workspace", "/workspace", "workspace root")
	archive := f.String("archive", "/archive", "read-only archive root")
	if err := f.Parse(args); err != nil {
		return err
	}
	t, err := agenttools.Open(*workspace, *archive, *organizationRoot)
	if err != nil {
		return err
	}
	defer t.Close()
	return t.Server().Run(ctx, &mcp.StdioTransport{})
}
