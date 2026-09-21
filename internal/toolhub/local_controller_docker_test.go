package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Opt-in checks use only a fresh no-account worker. Never use user session files.
func TestLocalControllerDockerPreparation(t *testing.T) {
	path := os.Getenv("HUB_LOCAL_CONTROLLER_CHECK_CONFIG")
	if path == "" {
		t.Skip("explicit operator-owned local preparation config required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config LocalControllerConfig
	if json.Unmarshal(body, &config) != nil {
		t.Fatal("invalid preparation config")
	}
	nonce := fmt.Sprintf("check-%d", time.Now().UnixNano())
	config.Context, config.Connection, config.MCPPort = nonce, nonce, 18546
	config.StateRoot = filepath.Join(config.StateRoot, "preparation-check", nonce)
	c, err := newLocalController(config)
	if err != nil {
		t.Fatal(err)
	}
	c.command = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "docker" && len(args) > 0 && args[0] == "update" {
			output, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
			if err != nil {
				t.Log("no-account update arguments", args, "result", string(output))
			}
			return output, err
		}
		return localCommand(ctx, binary, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = localCommand(cleanup, config.ToolHiveBinary, "rm", c.plan.WorkloadID)
		_, _ = localCommand(cleanup, "docker", "stop", c.plan.WorkloadID+"-proxy")
		_, _ = localCommand(cleanup, "docker", "rm", c.plan.WorkloadID+"-proxy")
		_, _ = localCommand(cleanup, "docker", "network", "rm", "hermes-"+c.plan.WorkloadID)
	})
	if err := c.start(ctx); err != nil {
		t.Fatal(err)
	}
	if receipt, err := c.inspect(ctx); err != nil || !receipt.Enforced {
		t.Fatal(receipt, err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "no-account-preparation-check", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://127.0.0.1:18546/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	names := map[string]bool{}
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names[tool.Name] = true
	}
	if len(names) != 5 || !names["get_account"] || !names["list_dialogs"] || !names["get_messages"] || !names["search_messages"] || !names["get_chat_info"] {
		t.Fatal(names)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_account", Arguments: map[string]any{}})
	if err != nil || !result.IsError {
		t.Fatal("unauthenticated account call did not fail closed", err)
	}
	if _, err := os.Stat(filepath.Join(c.plan.WorkspacePath, "credentials.env")); !os.IsNotExist(err) {
		t.Fatal("check must not use credentials")
	}
}
