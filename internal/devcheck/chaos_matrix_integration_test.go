//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type chaosFixtureConfig struct {
	Principal, Policy, Image, Network, Listen     string
	SupervisorURL, TelegramURL, BackendURL, Token string
}

// Re-execution runs production services with synthetic state and accounts.
func TestExecutionChaosMatrix(t *testing.T) {
	if configPath := os.Getenv("HUB_CHAOS_CHILD_CONFIG"); configPath != "" {
		runChaosService(t, configPath, os.Getenv("HUB_CHAOS_CHILD_ROLE"))
		return
	}
	imageName := os.Getenv("HERMES_CHAOS_IMAGE")
	if imageName == "" {
		t.Skip("set HERMES_CHAOS_IMAGE to the verified local image")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	linuxBinary := filepath.Join(t.TempDir(), "chaos-test")
	compile := exec.Command("go", "test", "-c", "-tags", "integration", "-o", linuxBinary, "./internal/devcheck")
	compile.Dir = repo
	compile.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v\n%s", err, out)
	}
	for _, target := range []string{"runtime", "toolhub", "communication-hub", "supervisor"} {
		for _, phase := range []string{"admission", "mid-run", "mid-delivery"} {
			t.Run(target+"/"+phase, func(t *testing.T) {
				runChaosCase(t, repo, linuxBinary, imageName, target, phase)
			})
		}
	}
	t.Run("heartbeat", func(t *testing.T) {
		runChaosCase(t, repo, linuxBinary, imageName, "heartbeat", "mid-run")
	})
}

func runChaosService(t *testing.T, configPath, role string) {
	t.Helper()
	var cfg chaosFixtureConfig
	body, err := os.ReadFile(configPath)
	if err != nil || json.Unmarshal(body, &cfg) != nil {
		t.Fatal("invalid fixture configuration")
	}
	root := filepath.Dir(configPath)
	switch role {
	case "supervisor":
		manager, err := supervisor.New(supervisor.Config{SpacesRoot: root, Image: cfg.Image, Network: cfg.Network, RuntimeAuth: "synthetic-chaos-control", PortBase: 32000, PortRange: 1000, WarmTTL: time.Minute, AllowWildcardListen: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal(supervisor.Serve(context.Background(), manager, cfg.Listen))
	case "communication-hub":
		gateway, err := communication.New(communication.Config{Supervised: true, OrganizationID: "personal", Users: []communication.User{{ID: cfg.Principal, RuntimeID: cfg.Principal, PolicyVersion: cfg.Policy, Enabled: true, TelegramIDs: []int64{21}, Features: []string{"workspace", "telegram"}}}, TelegramToken: "synthetic-bot", APIBaseURL: cfg.TelegramURL, SpoolDir: filepath.Join(root, "spool"), RuntimeURL: cfg.SupervisorURL, RuntimeAuth: "synthetic-chaos-control", PollTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal(gateway.Run(context.Background()))
	case "toolhub":
		auth := identity.TelegramEnvelope(cfg.Principal, 21, cfg.Principal, cfg.Policy)
		definition := gatewayProbeDefinition("chaos-counter", toolhub.RemoteMCP, "read", toolhub.Shared, false, "https://fixture.invalid/mcp", "")
		connection := toolhub.Connection{Schema: toolhub.SchemaVersion, ConnectionID: "chaos-connection", Owner: toolhub.OwnerRef{Type: toolhub.ContextOwner, ID: auth.ContextID}, DefinitionID: definition.DefinitionID, Revision: 1, Status: toolhub.ActiveStatus, Metadata: map[string]string{"mcp_endpoint": cfg.BackendURL}}
		store := toolhub.NewStore()
		for _, put := range []func() error{func() error { return store.RegisterDefinition(definition) }, func() error { return store.PutConnection(connection) }, func() error { return store.PutBinding(gatewayProbeBinding(auth, definition, connection)) }} {
			if err := put(); err != nil {
				t.Fatal(err)
			}
		}
		gateway := &toolhub.Gateway{Store: store, Backend: toolhub.MCPBackend{}, Tokens: map[string]identity.Envelope{cfg.Token: auth}, DisableLocalhostProtection: true}
		handler, err := gateway.Handler()
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Addr: ":8090", Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		t.Fatal(server.ListenAndServe())
	default:
		t.Fatal("unknown fixture role")
	}
}

func runChaosCase(t *testing.T, repo, linuxBinary, imageName, target, phase string) {
	t.Helper()
	timeout := 7 * time.Minute
	if target == "heartbeat" {
		timeout = 12 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	budget := "90s"
	if target == "heartbeat" {
		budget = "7m"
	}
	root := t.TempDir()
	t.Logf("isolated fixture: %s", root)
	name := "chaos-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	network := name + "-net"
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("fixture docker %s: %v\n%s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	docker("network", "create", network)
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })
	var effects atomic.Int32
	var effectMu sync.Mutex
	fixture := mcp.NewServer(&mcp.Implementation{Name: "chaos-counter", Version: "1"}, nil)
	fixture.AddTool(&mcp.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		effectMu.Lock()
		defer effectMu.Unlock()
		file, err := os.OpenFile(filepath.Join(root, "effects.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		if _, err := file.WriteString("effect\n"); err != nil {
			return nil, err
		}
		if err := file.Sync(); err != nil {
			return nil, err
		}
		effects.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "counted fixture effect"}}}, nil
	})
	backend, backendURL := chaosHTTP(t, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return fixture }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true}))
	defer backend.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var modelCalls atomic.Int32
	provider, providerURL, err := nativeToolHubProvider(toolhub.ProjectedToolName("chaos-counter", "1.0.0", "read"), &modelCalls, entered, release)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { provider.CloseClientConnections(); provider.Close() }()
	phaseEntered, phaseRelease := make(chan struct{}), make(chan struct{})
	var phaseOnce, phaseReleaseOnce sync.Once
	openPhase := func() { phaseReleaseOnce.Do(func() { close(phaseRelease) }) }
	defer openPhase()
	barrier := func(ctx context.Context) {
		phaseOnce.Do(func() { close(phaseEntered) })
		select {
		case <-phaseRelease:
		case <-ctx.Done():
		}
	}
	providerTarget, err := url.Parse(provider.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(providerTarget)
	modelProxy, modelURL := chaosHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			body, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
			if err != nil {
				http.Error(w, "fixture request unreadable", http.StatusBadRequest)
				return
			}
			var request struct{ Stream bool }
			if err := json.Unmarshal(body, &request); err != nil {
				http.Error(w, "fixture request invalid", http.StatusBadRequest)
				return
			}
			if !request.Stream {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"Synthetic title"},"finish_reason":"stop"}]}`)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if phase == "admission" && bytes.Contains(body, []byte("active-run")) {
				barrier(r.Context())
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	defer func() { modelProxy.CloseClientConnections(); modelProxy.Close() }()
	providerURL = modelURL + "/v1"
	if phase != "mid-run" {
		unblock()
	}
	var mu sync.Mutex
	var messages []string
	api, apiURL := chaosHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			_ = contractSleep(r.Context(), 100*time.Millisecond)
			_, _ = io.WriteString(w, "{\"ok\":true,\"result\":[]}")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var message struct{ Text string }
			_ = json.NewDecoder(r.Body).Decode(&message)
			mu.Lock()
			messages = append(messages, message.Text)
			mu.Unlock()
			if phase == "mid-delivery" && message.Text == "toolhub final answer" {
				barrier(r.Context())
			}
		}
		_, _ = io.WriteString(w, "{\"ok\":true,\"result\":{\"message_id\":1}}")
	}))
	defer func() { api.CloseClientConnections(); api.Close() }()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cfg := chaosFixtureConfig{Principal: name, Image: imageName, Network: network, Listen: fmt.Sprintf("0.0.0.0:%d", port), SupervisorURL: fmt.Sprintf("http://host.docker.internal:%d", port), TelegramURL: apiURL, BackendURL: backendURL}
	space := filepath.Join(root, name)
	if err := stack.InitEnvironment(space, name, "prod"); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"agent.yaml", "workspace.yaml"} {
		if err := os.Remove(filepath.Join(space, file)); err != nil {
			t.Fatal(err)
		}
	}
	chaosWrite(t, filepath.Join(space, "settings.yaml"), []byte("schema: 1\nuser: "+name+"\ntimezone: UTC\nbrowser_port: 6080\noauth_port: 8000\nfeatures: [workspace, telegram]\nmodel: gpt-4o-mini\nmodel_url: "+providerURL+"\n"))
	endpoint := "http://toolhub:8090/mcp"
	chaosWrite(t, filepath.Join(space, "secrets.prod.env"), []byte("OPENAI_API_KEY=synthetic-chaos-key\nTELEGRAM_BOT_TOKEN=synthetic-bot\nTELEGRAM_ALLOWED_USERS=21\nHUB_RUN_BUDGET="+budget+"\nHUB_TOOLHUB_ENDPOINT="+endpoint+"\n"))
	if err := stack.RenderEnvironment(space, repo, "prod"); err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEnvironment(space, "prod")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Policy = stack.PolicyVersion(settings)
	token, err := os.ReadFile(filepath.Join(space, "runtime.auth"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Token = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(token)), "HUB_RUNTIME_AUTH="))
	chaosWrite(t, filepath.Join(space, "hermes.prod.yaml"), []byte("model:\n  provider: custom\n  default: gpt-4o-mini\n  base_url: "+providerURL+"\nplatform_toolsets:\n  api_server: [hermes-api-server, toolhub]\nmcp_servers:\n  toolhub:\n    url: "+endpoint+"\n    timeout: 30\n    skip_preflight: false\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"))
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	chaosWrite(t, configPath, body)
	binaryBody, err := os.ReadFile(linuxBinary)
	if err != nil {
		t.Fatal(err)
	}
	chaosWrite(t, filepath.Join(root, "chaos-test"), binaryBody)
	spool, err := communication.NewSpool(filepath.Join(root, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := communication.Job{Envelope: identity.TelegramEnvelope(name, 21, name, cfg.Policy), ID: "chaos-job", IdempotencyKey: "chaos-job", OrganizationID: "personal", UserID: name, ActorID: name, ScopeID: "user:" + name, Channel: "telegram_bot", Trigger: "message", ChatID: 21, MessageID: 1, Text: "toolhub active-run", CreatedAt: time.Now().UTC()}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, 0777)
	}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	newHost := func() *exec.Cmd {
		return exec.Command(executable, "-test.run=^TestExecutionChaosMatrix$", "-test.timeout=15m")
	}
	host := newHost()
	host.Env = append(os.Environ(), "HUB_CHAOS_CHILD_CONFIG="+configPath, "HUB_CHAOS_CHILD_ROLE=supervisor", "HUB_RUN_BUDGET="+budget, "HUB_CANARY_INTERVAL=0")
	logFile, err := os.OpenFile(filepath.Join(root, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	host.Stdout, host.Stderr = logFile, logFile
	if err := host.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	logFile.Close()
	t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })
	containers := map[string]string{}
	for _, role := range []string{"toolhub", "communication-hub"} {
		id := docker("run", "-d", "--name", name+"-"+role, "--network", network, "--network-alias", role, "--add-host", "host.docker.internal:host-gateway", "--mount", "type=bind,src="+root+",dst=/fixture", "--entrypoint", "/fixture/chaos-test", "-e", "HUB_CHAOS_CHILD_CONFIG=/fixture/config.json", "-e", "HUB_CHAOS_CHILD_ROLE="+role, "-e", "HUB_RUN_BUDGET="+budget, "-e", "HUB_CANARY_INTERVAL=0", "-e", "HUB_DIAGNOSTICS_ENABLED=false", imageName, "-test.run=^TestExecutionChaosMatrix$", "-test.timeout=15m")
		containers[role] = id
		t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	}
	runtimeID := func() string {
		body, err := os.ReadFile(filepath.Join(root, ".control", "supervisor.json"))
		var ledger struct {
			Items []struct{ Runtime supervisor.Runtime }
		}
		if err != nil || json.Unmarshal(body, &ledger) != nil {
			return ""
		}
		for _, item := range ledger.Items {
			if item.Runtime.PrincipalID != name || item.Runtime.Generation == "" {
				continue
			}
			raw, err := exec.Command("docker", "inspect", "--format", "{{.Id}}", item.Runtime.Container).Output()
			if err != nil {
				continue
			}
			id := strings.TrimSpace(string(raw))
			raw, err = exec.Command("docker", "inspect", "--format", "{{json .Config.Labels}}", id).Output()
			var labels map[string]string
			owner := fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.Join(root, ".control", "supervisor.json"))))
			contextID := fmt.Sprintf("%x", sha256.Sum256([]byte(name+"\x00"+name+"\x00gateway")))
			if err == nil && json.Unmarshal(raw, &labels) == nil && labels["hermes-hub.owner"] == owner && labels["hermes-hub.context"] == contextID && labels["hermes-hub.generation"] == item.Runtime.Generation {
				return id
			}
		}
		return ""
	}
	t.Cleanup(func() {
		_ = host.Process.Kill()
		_ = host.Wait()
		_ = exec.Command("docker", "rm", "-f", containers["communication-hub"]).Run()
		if id := runtimeID(); id != "" {
			_ = exec.Command("docker", "rm", "-f", id).Run()
		}
		_ = exec.Command("docker", "volume", "rm", "hermes-hub-"+name+"-prod_broker-secrets-runtime").Run()
	})
	waitPhase := phaseEntered
	if phase == "mid-run" {
		waitPhase = entered
	}
	select {
	case <-waitPhase:
	case <-ctx.Done():
		for role, id := range containers {
			out, _ := exec.Command("docker", "logs", "--tail", "15", id).CombinedOutput()
			t.Logf("%s: %s", role, out)
		}
		body, _ := os.ReadFile(filepath.Join(root, "supervisor.log"))
		t.Logf("supervisor: %s", body)
		t.Fatalf("%s phase not reached; no kill performed", phase)
	}
	if phase != "admission" && effects.Load() != 1 {
		t.Fatalf("%s phase requires one completed effect; got %d; no kill performed", phase, effects.Load())
	}
	id := runtimeID()
	if id == "" {
		t.Fatal("fixture runtime unavailable; kill refused")
	}
	switch target {
	case "heartbeat":
		if err := contractSleep(ctx, 305*time.Second); err != nil {
			t.Fatal(err)
		}
		mapping, ok, err := spool.Mapping(job.ID)
		if err != nil || !ok || mapping.Status != "running" {
			t.Fatalf("long run lost while model was active: status=%s exists=%v error=%v", mapping.Status, ok, err)
		}
	case "runtime":
		docker("kill", id)
		docker("start", id)
	case "supervisor":
		if err := host.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = host.Wait()
		previous := host
		host = newHost()
		host.Env = previous.Env
		logFile, err := os.OpenFile(filepath.Join(root, "supervisor.log"), os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		host.Stdout, host.Stderr = logFile, logFile
		err = host.Start()
		logFile.Close()
		if err != nil {
			t.Fatal(err)
		}
	default:
		docker("kill", containers[target])
		docker("start", containers[target])
	}
	openPhase()
	unblock()
	var mapping communication.JobMapping
	countFinalSends := func() int {
		texts := map[string]bool{}
		files, _ := filepath.Glob(filepath.Join(root, "spool", "outbox", "*", "*.json"))
		for _, path := range files {
			body, _ := os.ReadFile(path)
			var delivery communication.Delivery
			if json.Unmarshal(body, &delivery) != nil || delivery.JobID != job.ID || (!strings.HasSuffix(delivery.ID, "-response") && !strings.HasSuffix(delivery.ID, "-error")) {
				continue
			}
			texts[delivery.Text] = true
		}
		mu.Lock()
		defer mu.Unlock()
		sends := 0
		for _, message := range messages {
			if texts[message] {
				sends++
			}
		}
		return sends
	}
	for ctx.Err() == nil {
		mapping, _, _ = spool.Mapping(job.ID)
		if mapping.Status == "completed" || mapping.Status == "failed" || mapping.Status == "interrupted" || mapping.Status == "cancelled" {
			if countFinalSends() > 0 {
				break
			}
		}
		_ = contractSleep(ctx, 200*time.Millisecond)
	}
	_ = contractSleep(ctx, 35*time.Second)
	sends := countFinalSends()
	effectMu.Lock()
	effectLog, _ := os.ReadFile(filepath.Join(root, "effects.log"))
	effectMu.Unlock()
	if bytes.Count(effectLog, []byte("effect\n")) != int(effects.Load()) {
		t.Fatal("durable effect journal disagrees with observed tool effects")
	}
	if ctx.Err() != nil || sends != 1 || effects.Load() > 1 || (phase != "admission" && effects.Load() != 1) || (mapping.Status == "completed" && effects.Load() != 1) {
		t.Fatalf("outcome=%s effects=%d sends=%d deadline=%v", mapping.Status, effects.Load(), sends, ctx.Err())
	}
	if mapping.Status != "completed" {
		if target == "heartbeat" {
			t.Fatalf("heartbeat run must complete, got %s", mapping.Status)
		}
		mu.Lock()
		named := false
		for _, message := range messages {
			named = named || (strings.Contains(message, "run."+mapping.Status) && strings.Contains(message, job.ID))
		}
		mu.Unlock()
		if !named {
			t.Fatal("terminal failure was not delivered with its named outcome and job ID")
		}
	}
	t.Logf("CHAOS PASS %s/%s outcome=%s effects=%d sends=%d", target, phase, mapping.Status, effects.Load(), sends)
}

func chaosHTTP(t *testing.T, handler http.Handler) (*httptest.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	return server, "http://host.docker.internal:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
}

func chaosWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
}
