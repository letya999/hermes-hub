package toolhub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type backendFunc func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error)

func (f backendFunc) Call(ctx context.Context, binding EffectiveBinding, tool ToolSpec, args map[string]any) (BackendResult, error) {
	return f(ctx, binding, tool, args)
}

func TestProjectedToolsUseCurrentAuthorization(t *testing.T) {
	store, auth, binding := seededStore(t)
	projected, err := store.ListProjectedTools(auth)
	if err != nil || len(projected) != 2 {
		t.Fatalf("projected tools=%+v err=%v", projected, err)
	}
	if _, effective, err := store.ResolveProjectedTool(auth, projected[0].Name); err != nil || effective.Binding.ToolBindingID != binding.ToolBindingID {
		t.Fatalf("projected resolve=%+v err=%v", effective, err)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if tools, err := store.ListProjectedTools(auth); err != nil || len(tools) != 0 {
		t.Fatalf("disabled projection=%+v err=%v", tools, err)
	}
	if _, _, err := store.ResolveProjectedTool(auth, projected[0].Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled call was not denied: %v", err)
	}
}

func TestProjectionAndLifecycleDenyPaths(t *testing.T) {
	store, auth, binding := seededStore(t)
	for _, name := range []string{"", "bad name", strings.Repeat("x", 129)} {
		if _, _, err := store.ResolveProjectedTool(auth, name); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid projected name %q: %v", name, err)
		}
	}
	badAuth := auth
	badAuth.Schema = 2
	if _, err := store.ListProjectedTools(badAuth); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid list identity: %v", err)
	}
	if _, _, err := store.ResolveProjectedTool(auth, "missing-tool"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing projected tool: %v", err)
	}
	shared := EffectiveBinding{Binding: ToolBinding{ContextID: "alice", WorkloadClass: Shared}, Definition: remoteDefinition()}
	shared.Definition.Workload.Class = Shared
	if _, err := OpenWorkloadWorkspace(t.TempDir(), shared, "job-1"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("shared job state accepted: %v", err)
	}
	if _, err := OpenWorkloadWorkspace(t.TempDir(), EffectiveBinding{Binding: binding, Definition: remoteDefinition()}, "job-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("per-user job state accepted: %v", err)
	}
	invalidClass := EffectiveBinding{Binding: binding, Definition: remoteDefinition()}
	invalidClass.Definition.Workload.Class = "unknown"
	if _, err := OpenWorkloadWorkspace(t.TempDir(), invalidClass, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown workload class accepted: %v", err)
	}
	perJob := EffectiveBinding{Binding: ToolBinding{ContextID: "alice", WorkloadClass: PerJob}, Definition: boundedCLIDefinition()}
	if _, err := OpenWorkloadWorkspace(t.TempDir(), perJob, "bad job"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid job id accepted: %v", err)
	}
}

func TestGatewayMCPWireRechecksBindingAndRejectsAuthorityArguments(t *testing.T) {
	store, auth, binding := seededStore(t)
	var calls atomic.Int32
	handler, err := (&Gateway{
		Store:  store,
		Tokens: map[string]identity.Envelope{"01234567890123456789012345678901": auth},
		Backend: backendFunc(func(_ context.Context, effective EffectiveBinding, tool ToolSpec, args map[string]any) (BackendResult, error) {
			calls.Add(1)
			if effective.Connection == nil || effective.Connection.Owner.ID != "alice" || tool.Name != "search" || args["query"] != "hello" {
				t.Fatalf("backend received untrusted or wrong binding: %+v %s %+v", effective, tool.Name, args)
			}
			return BackendResult{Text: "provider result"}, nil
		}),
	}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: server.URL + DefaultEndpointPath, HTTPClient: &http.Client{Transport: testBearerTransport{base: http.DefaultTransport, token: "01234567890123456789012345678901"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatalf("tools/list=%+v err=%v", tools, err)
	}
	name := ProjectedToolName("google-work", "1.0.0", "search")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "hello"}})
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("tools/call=%+v err=%v calls=%d", result, err, calls.Load())
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"connection_id": "foreign"}}); err == nil {
		t.Fatal("authority argument was accepted")
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{"query": "stale"}}); err == nil {
		t.Fatal("revoked binding was called through cached MCP session")
	}
}

func TestGatewayCallBoundsBackendResult(t *testing.T) {
	store, auth, binding := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	backendError := errors.New("backend unavailable")
	for _, tc := range []struct {
		name   string
		result BackendResult
		err    error
		want   error
	}{
		{name: "backend", err: backendError, want: backendError},
		{name: "text limit", result: BackendResult{Text: strings.Repeat("x", MaxOutputBytes+1)}, want: ErrInvalid},
		{name: "structured limit", result: BackendResult{Structured: strings.Repeat("x", MaxOutputBytes+1)}, want: ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
				return tc.result, tc.err
			})}
			_, err := gateway.call(context.Background(), auth, name, nil)
			if !errors.Is(err, tc.want) {
				t.Fatalf("gateway call error=%v want=%v", err, tc.want)
			}
		})
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, DisabledStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{}, nil
	})}).call(context.Background(), auth, name, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gateway did not recheck disabled binding: %v", err)
	}
}

type testBearerTransport struct {
	base  http.RoundTripper
	token string
	jobID string
	runID string
}

func (t testBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copyRequest := request.Clone(request.Context())
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	if t.jobID != "" {
		copyRequest.Header.Set(HeaderJobID, t.jobID)
	}
	if t.runID != "" {
		copyRequest.Header.Set(HeaderRunID, t.runID)
	}
	return t.base.RoundTrip(copyRequest)
}

func TestPrivateBackendEndpointValidation(t *testing.T) {
	for _, value := range []string{"http://127.0.0.1:4483/mcp", "http://vmcp/mcp"} {
		if err := ValidateBackendEndpoint(value); err != nil {
			t.Fatalf("private endpoint %q rejected: %v", value, err)
		}
	}
	embeddedUserinfo := (&url.URL{Scheme: "http", Host: "127.0.0.1", Path: "/mcp", User: url.UserPassword("user", "pass")}).String()
	for _, value := range []string{"https://evil.example/mcp", embeddedUserinfo, "http://127.0.0.1/mcp?token=secret"} {
		if err := ValidateBackendEndpoint(value); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", value)
		}
	}
}

func TestMCPBackendUsesPrivateMCPContract(t *testing.T) {
	backendServer := mcp.NewServer(&mcp.Implementation{Name: "backend-fixture", Version: "1"}, nil)
	backendServer.AddTool(&mcp.Tool{Name: "search", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "backend result"}}}, nil
	})
	backendHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backendServer }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer backend-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		backendHandler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	connection := *effective.Connection
	connection.Metadata = map[string]string{"mcp_endpoint": httpServer.URL + "/mcp"}
	effective.Connection = &connection
	result, err := (MCPBackend{HTTPClient: httpServer.Client(), Token: "backend-token"}).Call(context.Background(), effective, effective.Definition.Tools[0], map[string]any{})
	if err != nil || result.Text != "backend result" {
		t.Fatalf("MCP backend result=%+v err=%v", result, err)
	}
}

func TestMCPBackendDenyPathsAndBearer(t *testing.T) {
	if _, err := (MCPBackend{}).Call(context.Background(), EffectiveBinding{}, ToolSpec{Name: "search"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing connection accepted: %v", err)
	}
	connection := &Connection{Metadata: map[string]string{}}
	effective := EffectiveBinding{Connection: connection}
	if _, err := (MCPBackend{}).Call(context.Background(), effective, ToolSpec{Name: "search"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing backend endpoint accepted: %v", err)
	}
	if schema := cliInputSchema(ToolSpec{Name: "query", Arguments: []CLIArgument{{Name: "text", Flag: "--text", Type: "string", Required: true}}}); schema["additionalProperties"] != false {
		t.Fatalf("CLI schema permits undeclared arguments: %#v", schema)
	}
}

func TestContainerMCPRequiresExternalAdmission(t *testing.T) {
	definition := statefulContainerDefinition()
	effective := EffectiveBinding{Definition: definition, Connection: &Connection{Metadata: map[string]string{"mcp_endpoint": "http://127.0.0.1:4483/mcp"}}}
	if _, err := (MCPBackend{}).Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("container execution without controller was admitted: %v", err)
	}
	if _, err := (MCPBackend{Admission: func(context.Context, EffectiveBinding) error { return errors.New("limits unavailable") }}).Call(context.Background(), effective, definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("controller denial was not fail-closed: %v", err)
	}
}

func TestWorkloadWorkspacePersistenceAndJobCleanup(t *testing.T) {
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	userWorkspace, err := OpenWorkloadWorkspace(root, effective, "")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(userWorkspace.Path, "state")
	if err := os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := userWorkspace.Cleanup(); err != nil {
		t.Fatal(err)
	}
	restarted, err := OpenWorkloadWorkspace(root, effective, "")
	if err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(restarted.Path, "state")); err != nil || string(body) != "preserve" {
		t.Fatalf("per-user state did not survive restart: %q %v", body, err)
	}
	jobEffective := EffectiveBinding{Binding: ToolBinding{ContextID: "alice", WorkloadClass: PerJob}, Definition: boundedCLIDefinition()}
	job, err := OpenWorkloadWorkspace(root, jobEffective, "job-1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Path == "" || !containedPath(root, job.Path) {
		t.Fatalf("job workspace escaped root: %q", job.Path)
	}
	if err := job.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("per-job workspace survived cleanup: %v", err)
	}
}

func TestCLIRunnerRequiresIsolationAndUsesDirectArgs(t *testing.T) {
	definition := testCLIDefinition("go", []string{"version"})
	root := t.TempDir()
	runner := CLIRunner{Root: root, AllowedExecutables: map[string]bool{"go": true}}
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, nil, root); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing isolation was not denied: %v", err)
	}
	var checked atomic.Bool
	runner.Isolation = func(policy ExecutionPolicy, gotRoot, gotCwd string) error {
		checked.Store(policy.Egress[0] == "test.invalid" && gotRoot == root && gotCwd == root)
		return nil
	}
	result, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, nil, root)
	if err != nil || result.IsError || !checked.Load() || !strings.Contains(result.Text, "go version") {
		t.Fatalf("direct CLI result=%+v err=%v isolation=%v", result, err, checked.Load())
	}
	if _, err := cliArguments(nil, definition.Tools[0], map[string]any{"foreign": "value"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("undeclared CLI argument=%v", err)
	}
	if _, err := cliArguments(nil, definition.Tools[0], map[string]any{"query": "$(touch /tmp/pwned)"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("undeclared shell-like argument=%v", err)
	}
}

func TestCLIArgumentSchemaAndRunnerDenyPaths(t *testing.T) {
	tool := ToolSpec{Name: "query", Effect: ReadEffect, Arguments: []CLIArgument{
		{Name: "text", Flag: "--text", Type: "string", Required: true},
		{Name: "count", Flag: "--count", Type: "integer"},
		{Name: "ratio", Flag: "--ratio", Type: "number"},
		{Name: "verbose", Flag: "--verbose", Type: "boolean"},
	}}
	args, err := cliArguments([]string{"list"}, tool, map[string]any{"text": "hello", "count": float64(2), "ratio": 1.5, "verbose": false})
	if err != nil || strings.Join(args, " ") != "list --text hello --count 2 --ratio 1.5 --verbose=false" {
		t.Fatalf("CLI args=%v err=%v", args, err)
	}
	for _, values := range []map[string]any{
		{},
		{"text": 2},
		{"text": "hello", "count": "2"},
		{"text": "hello", "ratio": "1.5"},
		{"text": "hello", "verbose": "true"},
		{"text": "bad\nvalue"},
	} {
		if _, err := cliArguments(nil, tool, values); err == nil {
			t.Fatalf("invalid CLI args accepted: %#v", values)
		}
	}
	if got := redactOutput("token=secret", map[string]string{"TOKEN": "secret"}); got != "token=[REDACTED]" {
		t.Fatalf("output was not redacted: %q", got)
	}
	for _, command := range []string{"sh", "bash", "cmd", "powershell", "sh -c"} {
		if validCommand(command) {
			t.Fatalf("shell command accepted: %q", command)
		}
	}
	if !validCommand(os.Args[0]) || validCommand("bad;command") {
		t.Fatal("direct command validation regression")
	}
	invalid := boundedCLIDefinition()
	invalid.Tools[0].Arguments = []CLIArgument{{Name: "query", Flag: "-q", Type: "string"}}
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid CLI argument schema accepted")
	}

	root := t.TempDir()
	definition := testCLIDefinition("go", []string{"version"})
	runner := CLIRunner{Root: root, AllowedExecutables: map[string]bool{"go": true}, Isolation: func(ExecutionPolicy, string, string) error { return nil }}
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, nil, filepath.Join(root, "..")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("outside cwd accepted: %v", err)
	}
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, map[string]string{"not-allowed": "x"}, root); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid environment key accepted: %v", err)
	}
	listed := runner
	listed.AllowedEnvironment = map[string]bool{"GITLAB_TOKEN": true}
	if _, err := listed.Run(context.Background(), definition, definition.Tools[0], nil, map[string]string{"NOT_ALLOWED": "x"}, root); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("unallowlisted environment accepted: %v", err)
	}
	definition.Execution.OutputBytes = 1
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, nil, root); !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("CLI output limit was not enforced: %v", err)
	}
	definition.Transport = RemoteMCP
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, nil, root); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong CLI transport accepted: %v", err)
	}
}

func TestCLIRunnerChild(t *testing.T) {
	if os.Getenv("HUB_TEST_CLI_CHILD") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
}

func testCLIDefinition(command string, args []string) ToolDefinition {
	return ToolDefinition{
		Schema: SchemaVersion, DefinitionID: "test-cli", Version: "1.0.0", Transport: BoundedCLI,
		Source:    DefinitionSource{Command: command, Args: args},
		Workload:  WorkloadPolicy{Class: PerJob, Rationale: "test process is cleaned after one job"},
		Execution: ExecutionPolicy{TimeoutSeconds: 5, OutputBytes: 4096, CPUMillis: 100, MemoryMiB: 64, MaxPIDs: 8, Egress: []string{"test.invalid"}},
		Health:    HealthProbe{Kind: "exec", Value: command, TimeoutSeconds: 1},
		Tools:     []ToolSpec{{Name: "run", Effect: ReadEffect}},
	}
}

func TestCLIRunnerCancellationKillsProcessTree(t *testing.T) {
	definition := testCLIDefinition(os.Args[0], []string{"-test.run=^TestCLIRunnerChild$"})
	root := t.TempDir()
	runner := CLIRunner{Root: root, AllowedExecutables: map[string]bool{os.Args[0]: true}, AllowedEnvironment: map[string]bool{"HUB_TEST_CLI_CHILD": true}, Isolation: func(ExecutionPolicy, string, string) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, definition, definition.Tools[0], nil, map[string]string{"HUB_TEST_CLI_CHILD": "1"}, filepath.Clean(root))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("process tree was not terminated promptly: %s", elapsed)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("secret leaked in cancellation error")
	}
}

func TestGatewayCallHoldsAdmissionFence(t *testing.T) {
	store, auth, binding := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	entered := make(chan struct{})
	release := make(chan struct{})
	gateway := Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		close(entered)
		<-release
		return BackendResult{Text: "ok"}, nil
	})}
	done := make(chan error, 1)
	go func() {
		_, err := gateway.call(context.Background(), auth, name, map[string]any{"query": "hello"})
		done <- err
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- store.SetBindingStatus(binding.ToolBindingID, RevokedStatus) }()
	select {
	case err := <-revoked:
		t.Fatalf("revocation overtook admitted gateway call: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.call(context.Background(), auth, name, map[string]any{"query": "hello"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked binding still callable: %v", err)
	}
}

func TestGatewayAuditOmitsSecrets(t *testing.T) {
	store, auth, _ := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	var events []map[string]string
	gateway := Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{Text: "ok"}, nil
	}), Audit: func(event string, fields map[string]string) {
		copied := map[string]string{"event": event}
		for key, value := range fields {
			copied[key] = value
		}
		events = append(events, copied)
	}}
	if _, err := gateway.call(context.Background(), auth, name, map[string]any{"query": "super-secret-token"}); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected audit events")
	}
	for _, event := range events {
		blob := strings.Join([]string{event["event"], event["principal_id"], event["binding_id"], event["backend"], event["outcome"]}, " ")
		for _, key := range event {
			blob += " " + key
		}
		if strings.Contains(blob, "super-secret-token") || strings.Contains(blob, "local://") || strings.Contains(blob, "GOOGLE_TOKEN") {
			t.Fatalf("audit leaked secret material: %+v", event)
		}
		if event["principal_id"] != "alice" || event["binding_id"] == "" || event["backend"] != string(RemoteMCP) {
			t.Fatalf("audit missing identity: %+v", event)
		}
	}
}

func TestRoutingBackendDispatchesCLIAndMCP(t *testing.T) {
	var mcpCalls, cliCalls atomic.Int32
	backend := RoutingBackend{
		MCP: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			mcpCalls.Add(1)
			return BackendResult{Text: "mcp"}, nil
		}),
		CLI: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			cliCalls.Add(1)
			return BackendResult{Text: "cli"}, nil
		}),
	}
	cliEffective := EffectiveBinding{Definition: boundedCLIDefinition()}
	if result, err := backend.Call(context.Background(), cliEffective, cliEffective.Definition.Tools[0], nil); err != nil || result.Text != "cli" || cliCalls.Load() != 1 || mcpCalls.Load() != 0 {
		t.Fatalf("cli dispatch result=%+v err=%v mcp=%d cli=%d", result, err, mcpCalls.Load(), cliCalls.Load())
	}
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := backend.Call(context.Background(), effective, effective.Definition.Tools[0], nil); err != nil || result.Text != "mcp" || mcpCalls.Load() != 1 {
		t.Fatalf("mcp dispatch result=%+v err=%v mcp=%d", result, err, mcpCalls.Load())
	}
	if _, err := (RoutingBackend{}).Call(context.Background(), cliEffective, cliEffective.Definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("missing CLI backend error=%v", err)
	}
	runner := CLIRunner{Root: t.TempDir(), AllowedExecutables: map[string]bool{"glab": true}}
	if _, err := runner.Call(context.Background(), cliEffective, cliEffective.Definition.Tools[0], nil); !errors.Is(err, ErrIsolation) {
		t.Fatalf("host CLI without isolation error=%v", err)
	}
	if _, err := (RoutingBackend{CLI: backend.CLI}).Call(context.Background(), effective, effective.Definition.Tools[0], nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing MCP backend error=%v", err)
	}
}

type envCaptureBackend struct {
	env map[string]string
}

func (e *envCaptureBackend) Call(ctx context.Context, effective EffectiveBinding, tool ToolSpec, arguments map[string]any) (BackendResult, error) {
	return e.CallEnv(ctx, effective, tool, arguments, nil)
}

func (e *envCaptureBackend) CallEnv(_ context.Context, _ EffectiveBinding, _ ToolSpec, _ map[string]any, environment map[string]string) (BackendResult, error) {
	e.env = environment
	return BackendResult{Text: "mcp-env"}, nil
}

func TestRoutingBackendPassesEnvToMCP(t *testing.T) {
	capture := &envCaptureBackend{}
	backend := RoutingBackend{MCP: capture}
	store, auth, binding := seededStore(t)
	effective, err := store.Resolve(auth, binding.ToolBindingID)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_TOKEN": "from-admit"}
	result, err := backend.CallEnv(context.Background(), effective, effective.Definition.Tools[0], nil, env)
	if err != nil || result.Text != "mcp-env" || capture.env["GOOGLE_TOKEN"] != "from-admit" {
		t.Fatalf("mcp env not forwarded: result=%+v env=%v err=%v", result, capture.env, err)
	}
}

func TestCLIRunnerNilAllowlistAcceptsCredentialKeys(t *testing.T) {
	root := t.TempDir()
	runner := CLIRunner{Root: root, AllowedExecutables: map[string]bool{os.Args[0]: true}, Isolation: func(ExecutionPolicy, string, string) error { return nil }}
	definition := testCLIDefinition(os.Args[0], []string{"-test.run=^$"})
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, map[string]string{"not-valid": "x"}, root); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid env key accepted: %v", err)
	}
	if _, err := runner.Run(context.Background(), definition, definition.Tools[0], nil, map[string]string{"GOOGLE_TOKEN": "from-admit"}, root); errors.Is(err, ErrUnauthorized) {
		t.Fatalf("nil allowlist denied credential key: %v", err)
	}
}

func TestGatewayRejectsBadAuthenticationBeforeMCP(t *testing.T) {
	store, auth, _ := seededStore(t)
	validToken := "01234567890123456789012345678901"
	newHandler := func() http.Handler {
		handler, err := (&Gateway{Store: store, Tokens: map[string]identity.Envelope{validToken: auth}, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
			return BackendResult{}, nil
		})}).Handler()
		if err != nil {
			t.Fatal(err)
		}
		return handler
	}
	for _, tc := range []struct {
		path, authorization, origin string
		status                      int
	}{
		{path: "/wrong", authorization: "Bearer " + validToken, status: http.StatusNotFound},
		{path: DefaultEndpointPath, authorization: "", status: http.StatusUnauthorized},
		{path: DefaultEndpointPath, authorization: "Bearer wrong", status: http.StatusUnauthorized},
		{path: DefaultEndpointPath, authorization: "Bearer " + validToken, origin: "https://evil.example", status: http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodPost, tc.path, nil)
		request.Header.Set("Authorization", tc.authorization)
		request.Header.Set("Origin", tc.origin)
		response := httptest.NewRecorder()
		newHandler().ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Fatalf("gateway status=%d want=%d for %#v", response.Code, tc.status, tc)
		}
	}
	if _, err := (&Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{}, nil
	})}).Handler(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing token map accepted: %v", err)
	}
	if _, err := (&Gateway{Store: store, Tokens: map[string]identity.Envelope{"short": auth}, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		return BackendResult{}, nil
	})}).Handler(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short token accepted: %v", err)
	}
}
