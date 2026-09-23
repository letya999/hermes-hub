//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolHubHermesContract keeps Hermes and its state volume alive while the
// authenticated Go ToolHub endpoint is restarted. vMCP/ToolHive is an
// operator-provided external dependency, never a second catalog.
func ToolHubHermesContract(ctx context.Context) error {
	endpoint := os.Getenv("TOOLHIVE_VMCP_ENDPOINT")
	toolName := os.Getenv("TOOLHIVE_REMOTE_TOOL")
	if endpoint == "" && toolName == "" {
		fixture := mcp.NewServer(&mcp.Implementation{Name: "toolhub-hermes-fixture", Version: "1"}, nil)
		fixture.AddTool(&mcp.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture read ok"}}}, nil
		})
		fixtureHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
		defer fixtureHTTP.Close()
		endpoint, toolName = fixtureHTTP.URL, "read"
	} else if endpoint == "" || toolName == "" {
		return errors.New("set both TOOLHIVE_VMCP_ENDPOINT and TOOLHIVE_REMOTE_TOOL, or neither for the local fixture")
	}
	if err := toolhub.ValidateBackendEndpoint(endpoint); err != nil {
		return err
	}
	image := os.Getenv("HERMES_CONTRACT_IMAGE")
	if image == "" {
		return errors.New("set HERMES_CONTRACT_IMAGE to the pinned Hermes image")
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "contract-user", ExternalIdentityID: "contract-transport", ContextID: "contract-user", RuntimeID: "contract-runtime", ConversationID: "contract-conversation", DeliveryTargetID: "contract-delivery", PolicyVersion: "contract-policy"}
	definition := gatewayProbeDefinition("hermes-toolhub", toolhub.RemoteMCP, toolName, toolhub.Shared, false, "https://remote-fixture.invalid/mcp", "")
	connection := toolhub.Connection{Schema: toolhub.SchemaVersion, ConnectionID: "hermes-connection", Owner: toolhub.OwnerRef{Type: toolhub.ContextOwner, ID: auth.ContextID}, DefinitionID: definition.DefinitionID, Revision: 1, Status: toolhub.ActiveStatus, Metadata: map[string]string{"mcp_endpoint": endpoint}}
	binding := gatewayProbeBinding(auth, definition, connection)
	store := toolhub.NewStore()
	for _, record := range []func() error{func() error { return store.RegisterDefinition(definition) }, func() error { return store.PutConnection(connection) }, func() error { return store.PutBinding(binding) }} {
		if err := record(); err != nil {
			return err
		}
	}
	gatewayToken := os.Getenv("TOOLHUB_GATEWAY_TOKEN")
	if gatewayToken == "" {
		gatewayToken = strings.Repeat("h", 32)
	}
	backend := &countingBackend{backend: toolhub.MCPBackend{Token: os.Getenv("TOOLHIVE_VMCP_TOKEN")}}
	var activeGateway *toolhub.Gateway
	newGateway := func(port int) (string, *http.Server, net.Listener, int, error) {
		activeGateway = &toolhub.Gateway{Store: store, Backend: backend, Tokens: map[string]identity.Envelope{gatewayToken: auth}, DisableLocalhostProtection: true}
		handler, err := activeGateway.Handler()
		if err != nil {
			return "", nil, nil, 0, err
		}
		address := "0.0.0.0:0"
		if port != 0 {
			address = "0.0.0.0:" + strconv.Itoa(port)
		}
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return "", nil, nil, 0, err
		}
		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		go func() { _ = server.Serve(listener) }()
		actualPort := listener.Addr().(*net.TCPAddr).Port
		return "http://host.docker.internal:" + strconv.Itoa(actualPort) + toolhub.DefaultEndpointPath, server, listener, actualPort, nil
	}
	gatewayURL, gatewayServer, gatewayListener, gatewayPort, err := newGateway(0)
	if err != nil {
		return err
	}
	defer gatewayServer.Close()
	defer gatewayListener.Close()
	probeClient := mcp.NewClient(&mcp.Implementation{Name: "toolhub-hermes-preflight", Version: "0.2.0"}, nil)
	localGatewayURL := strings.Replace(gatewayURL, "host.docker.internal", "127.0.0.1", 1)
	probeSession, err := probeClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: localGatewayURL, HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: bearerTransport{token: gatewayToken}}}, nil)
	if err != nil {
		return fmt.Errorf("ToolHub preflight connect: %w", err)
	}
	listed, err := probeSession.ListTools(ctx, nil)
	_ = probeSession.Close()
	if err != nil || !hasMCPTool(listed.Tools, toolhub.ProjectedToolName(definition.DefinitionID, definition.Version, toolName)) {
		return fmt.Errorf("ToolHub preflight list failed: err=%v tools=%d", err, len(listed.Tools))
	}
	var modelToolCalls atomic.Int32
	activeRunEntered, activeRunRelease := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-activeRunRelease:
		default:
			close(activeRunRelease)
		}
	}()
	provider, providerURL, err := nativeToolHubProvider(toolhub.ProjectedToolName(definition.DefinitionID, definition.Version, toolName), &modelToolCalls, activeRunEntered, activeRunRelease)
	if err != nil {
		return err
	}
	defer func() { provider.CloseClientConnections(); provider.Close() }()
	suffix := make([]byte, 5)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	name := fmt.Sprintf("hermes-toolhub-contract-%x", suffix)
	volume := name + "-state"
	key := "fixture-auth-toolhub-contract-0123456789"
	run := func(args ...string) error { return dockerContract(ctx, args...) }
	out := func(args ...string) ([]byte, error) { return dockerContractOutput(ctx, args...) }
	defer func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() }()
	defer func() { _ = exec.Command("docker", "rm", "-f", name).Run() }()
	if err := run("volume", "create", volume); err != nil {
		return err
	}
	config := strings.Join([]string{
		"model:", "  provider: custom", "  default: gpt-4o-mini", "  base_url: " + providerURL,
		"platform_toolsets:", "  api_server:", "    - hermes-api-server", "    - toolhub",
		"mcp_servers:", "  toolhub:", "    url: " + gatewayURL, "    timeout: 120", "    skip_preflight: false",
		"    headers:", "      Authorization: Bearer ${HERMES_TOOLHUB_TOKEN}",
		"mcp_discovery_timeout: 30", "mcp_single_query_discovery_timeout: 30", "",
	}, "\n")
	encoded, _ := json.Marshal(config)
	python := "from pathlib import Path; p=Path('/state/hermes'); p.mkdir(parents=True,exist_ok=True); (p/'config.yaml').write_text(" + string(encoded) + ")"
	if err := run("run", "--rm", "--entrypoint", "python", "-v", volume+":/state", image, "-c", python); err != nil {
		return err
	}
	if err := run("run", "-d", "--name", name, "--entrypoint", "hermes", "--read-only", "--cap-drop", "ALL", "--add-host", "host.docker.internal:host-gateway", "--security-opt", "no-new-privileges:true", "--shm-size", "256m", "--tmpfs", "/tmp:mode=1777", "--tmpfs", "/workspace:uid=10001,gid=10001,mode=0700", "-v", volume+":/state", "-e", "HERMES_HOME=/state/hermes", "-e", "HOME=/state/home", "-e", "XDG_CONFIG_HOME=/state/config", "-e", "HERMES_TOOLHUB_TOKEN="+gatewayToken, "-e", "HERMES_GATEWAY_NO_TTY=true", "-e", "HERMES_DISABLE_LAZY_INSTALLS=1", "-e", "API_SERVER_ENABLED=true", "-e", "API_SERVER_KEY="+key, "-e", "API_SERVER_HOST=127.0.0.1", "-e", "API_SERVER_PORT="+hermesContractPort, "-e", "OPENAI_API_KEY=probe-openai-key-0123456789", "-e", "OPENAI_BASE_URL="+providerURL, image, "gateway", "run", "--no-supervise", "--force"); err != nil {
		return err
	}
	if err := waitHermesHealth(ctx, out, name, key); err != nil {
		return err
	}
	processID, err := out("inspect", "--format", "{{.State.Pid}}", name)
	if err != nil || len(strings.TrimSpace(string(processID))) == 0 {
		return fmt.Errorf("Hermes process identity unavailable: %w", err)
	}
	probeScript := "import httpx; r=httpx.post(" + strconv.Quote(gatewayURL) + ",headers={'Authorization':'Bearer " + gatewayToken + "','Accept':'application/json, text/event-stream','Content-Type':'application/json'},json={'jsonrpc':'2.0','id':1,'method':'initialize','params':{'protocolVersion':'2025-03-26','capabilities':{},'clientInfo':{'name':'probe','version':'1'}}},timeout=10); print(r.status_code, r.text[:200])"
	if probeResult, probeErr := out("exec", name, "python", "-c", probeScript); probeErr == nil {
		fmt.Printf("Hermes-container ToolHub HTTP preflight: %s", probeResult)
	}
	first, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"toolhub probe","session_id":"toolhub-session","provider":"custom","model":"gpt-4o-mini"}`, "toolhub-run-1")
	if err != nil || first.status != http.StatusAccepted {
		return fmt.Errorf("ToolHub Hermes run admission failed: HTTP %d", first.status)
	}
	firstID, _ := jsonString(first.body, "run_id")
	if err := waitHermesRunTerminal(ctx, out, name, key, firstID); err != nil {
		return err
	}
	completed, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/runs/"+firstID, "", "")
	if err != nil || !strings.Contains(string(completed.body), "completed") || backend.successes.Load() < 1 {
		return fmt.Errorf("Hermes did not complete a successful ToolHub call: attempts=%d successes=%d status=%d body=%s", backend.calls.Load(), backend.successes.Load(), completed.status, compactProbeBody(completed.body))
	}
	initialCalls, initialModelCalls := backend.calls.Load(), modelToolCalls.Load()
	if initialModelCalls == 0 {
		return errors.New("Hermes model did not select the ToolHub tool")
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, toolhub.DisabledStatus); err != nil {
		return err
	}
	if err := activeGateway.RefreshProjection(); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := out("exec", name, "sh", "-c", "grep -q 'received tools/list_changed notification' /state/hermes/logs/agent.log"); err != nil {
		return errors.New("Hermes did not receive ToolHub tools/list_changed")
	}
	if _, err := out("exec", name, "sh", "-c", "grep -q 'tools changed dynamically.*removed' /state/hermes/logs/agent.log"); err != nil {
		return errors.New("Hermes received ToolHub notification but did not remove the stale tool")
	}
	disabled, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"toolhub probe","session_id":"toolhub-session","provider":"custom","model":"gpt-4o-mini"}`, "toolhub-run-disabled")
	if err != nil || disabled.status != http.StatusAccepted {
		return fmt.Errorf("disabled ToolHub Hermes run admission failed: HTTP %d", disabled.status)
	}
	disabledID, _ := jsonString(disabled.body, "run_id")
	if err := waitHermesRunTerminal(ctx, out, name, key, disabledID); err != nil {
		return err
	}
	if backend.calls.Load() != initialCalls || modelToolCalls.Load() != initialModelCalls {
		return fmt.Errorf("Hermes retained the removed ToolHub tool: backend=%d model=%d", backend.calls.Load()-initialCalls, modelToolCalls.Load()-initialModelCalls)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, toolhub.ActiveStatus); err != nil {
		return err
	}
	if err := activeGateway.RefreshProjection(); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	restored, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"toolhub probe","session_id":"toolhub-session","provider":"custom","model":"gpt-4o-mini"}`, "toolhub-run-restored")
	if err != nil || restored.status != http.StatusAccepted {
		return fmt.Errorf("restored ToolHub Hermes run admission failed: HTTP %d", restored.status)
	}
	restoredID, _ := jsonString(restored.body, "run_id")
	if err := waitHermesRunTerminal(ctx, out, name, key, restoredID); err != nil {
		return err
	}
	if backend.successes.Load() < 2 || modelToolCalls.Load() <= initialModelCalls {
		return fmt.Errorf("Hermes did not discover the restored ToolHub tool: successes=%d model=%d", backend.successes.Load(), modelToolCalls.Load())
	}
	active, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"toolhub active-run probe","session_id":"toolhub-session","provider":"custom","model":"gpt-4o-mini"}`, "toolhub-run-active")
	if err != nil || active.status != http.StatusAccepted {
		return fmt.Errorf("active ToolHub Hermes run admission failed: HTTP %d", active.status)
	}
	activeID, _ := jsonString(active.body, "run_id")
	select {
	case <-activeRunEntered:
	case <-time.After(30 * time.Second):
		return errors.New("Hermes active run did not reach the final model response")
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, toolhub.DisabledStatus); err != nil {
		return err
	}
	if err := activeGateway.RefreshProjection(); err != nil {
		return err
	}
	close(activeRunRelease)
	if err := waitHermesRunTerminal(ctx, out, name, key, activeID); err != nil {
		return fmt.Errorf("ToolHub projection change interrupted the active Hermes run: %w", err)
	}
	activeResult, err := hermesHTTP(ctx, out, name, key, http.MethodGet, "/v1/runs/"+activeID, "", "")
	if err != nil || !strings.Contains(string(activeResult.body), "completed") {
		return errors.New("Hermes active run did not complete after projection change")
	}
	currentPID, err := out("inspect", "--format", "{{.State.Pid}}", name)
	if err != nil || string(currentPID) != string(processID) {
		return errors.New("Hermes process changed during ToolHub projection refresh")
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, toolhub.ActiveStatus); err != nil {
		return err
	}
	if err := activeGateway.RefreshProjection(); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_ = gatewayServer.Shutdown(shutdownCtx)
	cancel()
	_ = gatewayListener.Close()
	gatewayURL, gatewayServer, gatewayListener, gatewayPort, err = newGateway(gatewayPort)
	if err != nil {
		return err
	}
	defer gatewayServer.Close()
	defer gatewayListener.Close()
	// The endpoint is restarted on the same port-independent transport
	// boundary; Hermes keeps the same container, session and state volume.
	second, err := hermesHTTP(ctx, out, name, key, http.MethodPost, "/v1/runs", `{"input":"toolhub reconnect probe","session_id":"toolhub-session","provider":"custom","model":"gpt-4o-mini"}`, "toolhub-run-2")
	if err != nil || second.status != http.StatusAccepted {
		return fmt.Errorf("ToolHub Hermes reconnect run admission failed: HTTP %d", second.status)
	}
	secondID, _ := jsonString(second.body, "run_id")
	if err := waitHermesRunTerminal(ctx, out, name, key, secondID); err != nil {
		return err
	}
	if backend.successes.Load() < 4 {
		return fmt.Errorf("ToolHub Hermes reconnect did not complete backend four times: attempts=%d successes=%d", backend.calls.Load(), backend.successes.Load())
	}
	if err := expectHermesHTTP(ctx, out, name, key, http.MethodGet, "/api/sessions/toolhub-session", http.StatusOK); err != nil {
		return fmt.Errorf("Hermes session was not preserved across ToolHub restart: %w", err)
	}
	fmt.Printf("Real Hermes ToolHub dynamic refresh passed: attempts=%d successes=%d session=preserved pid=preserved active_run=completed endpoint=%s\n", backend.calls.Load(), backend.successes.Load(), endpoint)
	return nil
}

func compactProbeBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 1024 {
		return text[:1024]
	}
	return text
}

type countingBackend struct {
	backend   toolhub.ToolBackend
	calls     atomic.Int32
	successes atomic.Int32
}

func nativeToolHubProvider(projectedName string, modelToolCalls *atomic.Int32, activeRunEntered, activeRunRelease chan struct{}) (*httptest.Server, string, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, "", err
	}
	var activeRunOnce sync.Once
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		if err != nil {
			http.Error(w, "unreadable provider request", http.StatusBadRequest)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
			Tools  []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role      string          `json:"role"`
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if json.Unmarshal(body, &request) != nil {
			http.Error(w, "invalid provider request", http.StatusBadRequest)
			return
		}
		input := ""
		lastTool := ""
		var lastToolResult json.RawMessage
		toolReturned := false
		for _, message := range request.Messages {
			if message.Role == "user" {
				input = string(message.Content)
				lastTool, lastToolResult, toolReturned = "", nil, false
			}
			for _, call := range message.ToolCalls {
				lastTool = call.Function.Name
			}
			if message.Role == "tool" {
				lastToolResult = message.Content
				toolReturned = true
			}
		}
		mcpToolName := "mcp__toolhub__" + strings.NewReplacer("-", "_", ".", "_", "/", "_").Replace(projectedName)
		searchComplete := lastTool == "tool_search" && toolSearchHit(lastToolResult, mcpToolName)
		searchMiss := lastTool == "tool_search" && toolReturned && !searchComplete
		actualResult := lastTool == "tool_call" && toolReturned
		if strings.Contains(input, "active-run") && actualResult {
			activeRunOnce.Do(func() { close(activeRunEntered) })
			select {
			case <-activeRunRelease:
			case <-r.Context().Done():
				return
			}
		}
		if strings.Contains(input, "toolhub") && request.Stream && !actualResult && !searchMiss {
			callName := "tool_search"
			callArguments, _ := json.Marshal(map[string]any{"queries": []string{mcpToolName}})
			if searchComplete {
				callName = "tool_call"
				callArguments, _ = json.Marshal(map[string]any{"name": mcpToolName, "arguments": map[string]any{}})
				modelToolCalls.Add(1)
			}
			toolCall := map[string]any{"index": 0, "id": "toolhub-probe", "type": "function", "function": map[string]any{"name": callName, "arguments": string(callArguments)}}
			message := map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{toolCall}}
			w.Header().Set("Content-Type", "text/event-stream")
			chunk, _ := json.Marshal(map[string]any{"id": "toolhub-probe", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": "tool_calls"}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"toolhub-probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"toolhub final answer\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"toolhub-probe\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	provider.Listener = listener
	provider.Start()
	providerURL := "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port) + "/v1"
	return provider, providerURL, nil
}

func toolSearchHit(content json.RawMessage, name string) bool {
	var text string
	if json.Unmarshal(content, &text) != nil {
		text = string(content)
	}
	var result struct {
		Tools map[string]json.RawMessage `json:"tools"`
	}
	if json.Unmarshal([]byte(text), &result) != nil {
		return false
	}
	_, found := result.Tools[name]
	return found
}

func (b *countingBackend) Call(ctx context.Context, binding toolhub.EffectiveBinding, tool toolhub.ToolSpec, args map[string]any) (toolhub.BackendResult, error) {
	b.calls.Add(1)
	result, err := b.backend.Call(ctx, binding, tool, args)
	if err == nil && !result.IsError {
		b.successes.Add(1)
	}
	return result, err
}
