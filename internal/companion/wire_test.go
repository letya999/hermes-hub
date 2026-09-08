package companion

import (
	"context"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompanionChild(t *testing.T) {
	if os.Getenv("HUB_TEST_CHILD") != "1" {
		return
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "child", Version: "1"}, nil)
	type Args struct {
		Text string `json:"text"`
	}
	for _, name := range []string{"echo", "excluded"} {
		mcp.AddTool(s, &mcp.Tool{Name: name, Description: name}, func(_ context.Context, _ *mcp.CallToolRequest, a Args) (*mcp.CallToolResult, Args, error) {
			return nil, a, nil
		})
	}
	err := s.Run(context.Background(), &mcp.StdioTransport{})
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}
func TestNativeWireAndAllowlist(t *testing.T) {
	t.Setenv("HUB_TEST_CHILD", "1")
	token := "01234567890123456789012345678901"
	t.Setenv("HUB_TEST_TOKEN", token)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	cfg := Config{Listen: addr, TokenEnv: "HUB_TEST_TOKEN", Command: []string{os.Args[0], "-test.run=^TestCompanionChild$"}, AllowedTools: []string{"echo"}}
	b, _ := yaml.Marshal(cfg)
	file := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(file, b, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, file) }()
	ready := false
	for range 100 {
		resp, err := http.Get("http://" + addr + "/mcp")
		if err == nil {
			_ = resp.Body.Close()
			ready = true
			break
		}
		select {
		case err := <-done:
			t.Fatal(err)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("bridge startup timeout")
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "wire-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: "http://" + addr + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}, Timeout: 5 * time.Second}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatal(tools, err)
	}
	out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "through native bridge"}})
	if err != nil || out.IsError {
		t.Fatal(out, err)
	}
	if fmt.Sprint(out.StructuredContent) == "" {
		t.Fatal("empty response")
	}
	_ = session.Close()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bridge failed graceful shutdown")
	}
}
