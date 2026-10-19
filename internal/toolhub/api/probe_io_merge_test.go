package api

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// fakeMCPChild speaks just enough MCP stdio for runMCPProbeIO: it consumes the
// initialize/initialized/tools-list frames and answers either a real
// tools/list result or a JSON-RPC error, selected by HUB_FAKE_MCP.
func fakeMCPCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestFakeMCPChildProcess")
	cmd.Env = append(os.Environ(), "HUB_FAKE_MCP="+mode)
	return cmd
}

func TestFakeMCPChildProcess(t *testing.T) {
	mode := os.Getenv("HUB_FAKE_MCP")
	if mode == "" {
		t.Skip("helper process")
	}
	reader := bufio.NewReader(os.Stdin)
	for i := 0; i < 3; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
	}
	switch mode {
	case "ok":
		fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"fake_tool","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}}`)
	case "rpc-error":
		fmt.Println(`{"jsonrpc":"2.0","id":2,"error":{"message":"auth refused"}}`)
	case "garbage":
		fmt.Println("server banner")
		fmt.Println(`{"level":"info","msg":"boot"}`)
		fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"fake_tool","inputSchema":{"type":"object"}}]}}`)
	}
}

// runMCPProbeIO drives initialize + tools/list against a child process; a
// fake MCP server exercises the happy path, an RPC error, and the noise-skip
// path without docker.
func TestRunMCPProbeIO(t *testing.T) {
	t.Parallel()
	tools, err := runMCPProbeIO(fakeMCPCommand(t, "ok"), nil)
	if err != nil || len(tools) != 1 || tools[0].Name != "fake_tool" || tools[0].Effect != ReadEffect {
		t.Fatalf("happy path: %v %+v", err, tools)
	}
	tools, err = runMCPProbeIO(fakeMCPCommand(t, "garbage"), nil)
	if err != nil || len(tools) != 1 || tools[0].Effect != WriteEffect {
		t.Fatalf("noise lines must be skipped, no annotation = write: %v %+v", err, tools)
	}
	if _, err = runMCPProbeIO(fakeMCPCommand(t, "rpc-error"), nil); err == nil {
		t.Fatal("rpc error must surface")
	}
	cmd := exec.CommandContext(context.Background(), "definitely-not-a-binary-hub")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if _, err = runMCPProbeIO(cmd, nil); err == nil {
		t.Fatal("spawn failure must error")
	}
}
