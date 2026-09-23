package toolhub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTelegramDefinitionAndReceiptBoundary(t *testing.T) {
	for _, write := range []bool{false, true} {
		d, err := TelegramDefinition(statefulContainerDefinition(), write)
		// Adapter images own their entrypoint, arbitrary command overrides denied.
		if err == nil {
			t.Fatal("command override accepted")
		}
		deployment := statefulContainerDefinition()
		deployment.Source.Command = ""
		d, err = TelegramDefinition(deployment, write)
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range d.Tools {
			if !telegramTool(tool.Name, tool.Effect) {
				t.Fatal(tool)
			}
		}
		if telegramTool("raw_api", ReadEffect) || telegramTool("send_message", ReadEffect) {
			t.Fatal("unknown or misclassified tool")
		}
	}
	d := statefulContainerDefinition()
	d.Source.Command = ""
	d, _ = TelegramDefinition(d, true)
	e := EffectiveBinding{Definition: d, Binding: ToolBinding{PrincipalID: "alice", ContextID: "alice"}, Connection: &Connection{ConnectionID: "telegram", Metadata: map[string]string{"telegram_account": "10"}}, WorkloadID: "workload"}
	env := map[string]string{"TELEGRAM_ACCOUNT_ID": "10", "TELEGRAM_WRITE": "true"}
	tool := ToolSpec{Name: "send_message", Effect: WriteEffect}
	for _, badEnv := range []map[string]string{nil, {"TELEGRAM_ACCOUNT_ID": "999", "TELEGRAM_WRITE": "true"}, {"TELEGRAM_ACCOUNT_ID": "10", "TELEGRAM_WRITE": "false"}} {
		if _, err := (MCPBackend{}).CallEnv(context.Background(), e, tool, nil, badEnv); err == nil {
			t.Fatal("bad credential admitted")
		}
	}
	var response any
	server := mcp.NewServer(&mcp.Implementation{Name: "telegram-fixture", Version: "1.0.0"}, nil)
	for _, name := range []string{"send_message", "reply_message", "delete_message"} {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{StructuredContent: response, Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
		})
	}
	fixture := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
	defer fixture.Close()
	e.Connection.Metadata["mcp_endpoint"] = fixture.URL + "/mcp"
	b := MCPBackend{Root: t.TempDir(), AdmissionVerifier: func(context.Context, EffectiveBinding) (AdmissionReceipt, error) {
		return AdmissionReceipt{WorkloadID: e.WorkloadID, State: "running", Enforced: true, ImageDigest: d.Source.Digest, SidecarImages: d.Workload.SidecarImages, Execution: d.Execution}, nil
	}}
	for _, name := range []string{"send_message", "reply_message", "delete_message"} {
		tool.Name = name
		args := map[string]any{"peer_id": 7, "target_id": 42}
		for _, bad := range []any{nil, map[string]any{"peer_id": 7}, map[string]any{"peer_id": 7, "message_id": 42, "receipt": "fake"}, map[string]any{"peer_id": 8, "message_id": 42, "receipt": "10:8:42"}} {
			response = bad
			if _, err := b.CallEnv(context.Background(), e, tool, args, env); err == nil {
				t.Fatal("missing/wrong receipt accepted")
			}
		}
		response = map[string]any{"peer_id": 7, "message_id": 42, "receipt": "10:7:42", "pts": 123}
		if name == "delete_message" {
			response.(map[string]any)["receipt"] = "10:7:42:pts:123"
		}
		result, err := b.CallEnv(context.Background(), e, tool, args, env)
		if err != nil || result.Receipt == "" {
			t.Fatalf("receipt=%+v err=%v", result, err)
		}
	}
}

func TestWorkloadConnectionIsolationAndConcurrentInject(t *testing.T) {
	store, auth, binding := seededStore(t)
	e, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	one, err := OpenWorkloadWorkspace(root, e, "")
	if err != nil {
		t.Fatal(err)
	}
	other := e
	copyConnection := *e.Connection
	copyConnection.ConnectionID = "other"
	other.Connection = &copyConnection
	two, err := OpenWorkloadWorkspace(root, other, "")
	if err != nil || one.Path == two.Path {
		t.Fatal("connections share files")
	}
	wipe, err := writeAuthorizedFiles(context.Background(), root, e, map[string]string{"TOKEN": "first"})
	if err != nil {
		t.Fatal(err)
	}
	defer wipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := writeAuthorizedFiles(ctx, root, e, map[string]string{"TOKEN": "second"}); err == nil {
		t.Fatal("concurrent injection overwritten")
	}
	raw, err := os.ReadFile(filepath.Join(one.Path, "credentials.env"))
	if err != nil || string(raw) != "TOKEN=first" {
		t.Fatal(string(raw), err)
	}
	if err := wipe(); err != nil {
		t.Fatal(err)
	}
	second, err := writeAuthorizedFiles(context.Background(), root, e, map[string]string{"TOKEN": "second"})
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	bad := dummyTelegramDeployment()
	bad.Source.Args = []string{"arbitrary"}
	if _, err := TelegramDefinition(bad, false); err == nil {
		t.Fatal("entrypoint args override accepted")
	}
}

func dummyTelegramDeployment() ToolDefinition {
	d := statefulContainerDefinition()
	d.Source.Command = ""
	return d
}

// Opt-in Docker gate exercises the actual Python MCP protocol, not a Go stand-in.
func TestTelegramPythonContainerStdio(t *testing.T) {
	image := os.Getenv("HUB_TELEGRAM_TEST_IMAGE")
	if image == "" {
		t.Skip("explicit built adapter image required")
	}
	if !digestPattern.MatchString(image) {
		t.Fatal("test image must be a real image digest")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "credentials.env"), []byte("TELEGRAM_API_ID=1\nTELEGRAM_API_HASH=fixture\nTELEGRAM_SESSION_STRING=fixture\nTELEGRAM_ACCOUNT_ID=10\nTELEGRAM_WRITE=false"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.Command("docker", "run", "--rm", "-i", "--network", "none", "--read-only", "--mount", "type=bind,source="+root+",target=/run/connector,readonly", image)
	client := mcp.NewClient(&mcp.Implementation{Name: "shipped-python-check", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: command}, nil)
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
		t.Fatal("read-only server allowlist", names)
	}
}
