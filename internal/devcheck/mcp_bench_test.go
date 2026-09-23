package devcheck

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPBench(t *testing.T) {
	type args struct {
		Message string `json:"message"`
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "bench-test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, in args) (*mcp.CallToolResult, args, error) {
		return nil, in, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	var out bytes.Buffer
	if err := MCPBench(context.Background(), httpServer.URL, "echo", 2, &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("missing result")
	}
	t.Setenv("HUB_MCP_BENCH_ARGS", "{")
	if err := MCPBench(context.Background(), httpServer.URL, "echo", 2, &out); err == nil {
		t.Fatal("accepted invalid benchmark arguments")
	}
	for _, calls := range []int{1, 10001} {
		if err := MCPBench(context.Background(), httpServer.URL, "echo", calls, &out); err == nil {
			t.Fatalf("accepted %d calls", calls)
		}
	}
}
