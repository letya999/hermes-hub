package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

// tools-exec is the private per-call agent-tools executor. The ToolHub
// gateway reaches it over a channel the agent cannot invoke (docker exec);
// it is never an MCP server and exposes no tool surface of its own. One
// request on stdin, one bounded result on stdout, then the process exits.
const toolsExecMaxRequest = 8 << 20

// openExecTools opens the executor's filesystem roots the same way for the
// one-shot tools-exec and the persistent tools-daemon.
func openExecTools() (*agenttools.Tools, error) {
	workspace := os.Getenv("HUB_WORKSPACE")
	if workspace == "" {
		workspace = "/workspace"
	}
	openOptional := func(path string) string {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			return path
		}
		return ""
	}
	return agenttools.OpenRoots(workspace, openOptional("/archive"), openOptional("/org"))
}

// execRequest dispatches one admitted request through the shared Tools
// instance; per-request timeout comes from the admitted frame.
func execRequest(ctx context.Context, t *agenttools.Tools, request toolhub.AgentExecRequest) toolhub.AgentExecResult {
	if request.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutSeconds)*time.Second)
		defer cancel()
	}
	out, callErr := t.ExecCall(ctx, request.Tool, request.Arguments, request.Scopes)
	result := toolhub.AgentExecResult{Result: out}
	if callErr != nil {
		result = toolhub.AgentExecResult{Error: callErr.Error()}
	}
	return result
}

func runToolsExec(ctx context.Context) error {
	var request toolhub.AgentExecRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, toolsExecMaxRequest)).Decode(&request); err != nil {
		return fmt.Errorf("tools-exec request: %w", err)
	}
	t, err := openExecTools()
	if err != nil {
		return fmt.Errorf("tools-exec open: %w", err)
	}
	defer t.Close()
	result := execRequest(ctx, t, request)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return fmt.Errorf("tools-exec response: %w", err)
	}
	return nil
}
