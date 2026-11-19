//go:build integration

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/identityhub"
	"github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This leg uses production ToolHub startup/clock, supervisor HTTP/state and
// managed Hermes/relay containers. Only the model and external channel are fixtures.
func TestM32RealLifecycleMemoryIsolation(t *testing.T) {
	image, environment := m32ContainerInputs(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	users := []string{"m32a" + suffix, "m32b" + suffix}
	var prompts [2]atomic.Bool
	provider, release, entered := m32HeldProvider(t, func(messages json.RawMessage) {
		for i, user := range users {
			if strings.Contains(string(messages), "M32 private recall "+user) && strings.Contains(string(messages), "M32-request-instruction-marker-"+user) {
				prompts[i].Store(true)
				if !strings.Contains(string(messages), "M32-private-marker-"+user) || strings.Contains(string(messages), "M32-private-marker-"+users[1-i]) {
					t.Error("private model prompt omitted own memory or included foreign memory")
				}
			}
		}
	})
	defer release()
	providerPort := provider.Listener.Addr().(*net.TCPAddr).Port
	root := t.TempDir()
	f, err := devcheck.NewM32Fixture(root, environment, fmt.Sprintf("http://host.docker.internal:%d/v1", providerPort), suffix)
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, image, environment, f)
	stopToolHub := h.startToolHub(t, ctx)
	t.Cleanup(func() { stopToolHub() })

	// File probes must work before any model job has warmed either runtime.
	for _, user := range f.Users {
		h.call(t, ctx, user, api.RunContext{}, "file_write", map[string]any{"root": "workspace", "path": "artifacts/documents/m32-cold.txt", "text": "M32-cold-owner-" + user}, true)
	}
	for i, user := range f.Users {
		body := h.call(t, ctx, user, api.RunContext{}, "file_read", map[string]any{"root": "workspace", "path": "artifacts/documents/m32-cold.txt"}, true)
		if !strings.Contains(body, "M32-cold-owner-"+user) || strings.Contains(body, "M32-cold-owner-"+f.Users[1-i]) {
			t.Fatal("cold executor file crossed its owner's workspace")
		}
		h.call(t, ctx, user, api.RunContext{}, "file_remove", map[string]any{"root": "workspace", "path": "artifacts/documents/m32-cold.txt"}, true)
	}
	// The provider holds two actual runs open concurrently.
	var runs [2]api.RunContext
	outcomes := make(chan runtime.ExecuteResponse, 2)
	errors := make(chan error, 2)
	for i, user := range f.Users {
		job := runtime.ExecuteRequest{Envelope: f.Auth[user], JobID: "m32-private-" + strconv.Itoa(i), IdempotencyKey: "m32-private-" + strconv.Itoa(i), OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", Trigger: "message", Text: "M32 private recall " + user, Instructions: "M32-request-instruction-marker-" + user}
		go func() {
			response, err := h.execute(ctx, job)
			outcomes <- response
			errors <- err
		}()
	}
	for !prompts[0].Load() || !prompts[1].Load() {
		select {
		case <-entered:
		case err := <-errors:
			t.Fatalf("real run ended before overlapping model requests: %v", err)
		case <-ctx.Done():
			t.Fatal("two real model requests did not overlap")
		}
	}
	for i := range f.Users {
		if !prompts[i].Load() {
			t.Fatal("the actual private model prompt was not observed")
		}
		runs[i] = h.waitRun(t, ctx, "m32-private-"+strconv.Itoa(i))
		if runs[i].Status != "running" || runs[i].RunID == "" || runs[i].RuntimeGeneration != runs[i].CurrentGeneration {
			t.Fatal("supervisor did not bind the active run to its current generation")
		}
	}
	if runs[0].RunID == runs[1].RunID {
		t.Fatal("private users share a run ID")
	}
	for i, user := range f.Users {
		other := 1 - i
		own := h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32-private-marker-" + user}, true)
		if !strings.Contains(own, "M32-private-marker-"+user) {
			t.Fatal("private owner cannot recall its seeded memory")
		}
		foreign := h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32-private-marker-" + f.Users[other]}, true)
		if strings.Contains(foreign, "M32-private-marker-"+f.Users[other]) {
			t.Fatal("memory search crossed the user boundary")
		}
		h.call(t, ctx, user, runs[other], "memory_search", map[string]any{"query": "M32"}, false)
		wrongRun := runs[i]
		wrongRun.RunID = runs[other].RunID
		h.call(t, ctx, user, wrongRun, "memory_search", map[string]any{"query": "M32"}, false)
		h.call(t, ctx, user, api.RunContext{}, "memory_search", map[string]any{"query": "M32"}, false)
		h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32", "principal_id": f.Users[other]}, false)
		remember := map[string]any{"op": "remember", "name": "M32 acceptance note", "text": "M32-remembered-" + user}
		h.call(t, ctx, user, runs[i], "memory_card", remember, true)
		h.call(t, ctx, user, runs[i], "memory_card", remember, true)
		note, err := os.ReadFile(filepath.Join(f.Root, "vaults", user, "memory", "cards", "notes", "inbox", "m32-acceptance-note.md"))
		m32Require(t, err)
		if strings.Count(string(note), "M32-remembered-"+user) != 1 {
			t.Fatal("remember retry duplicated or lost the owner write")
		}
		h.call(t, ctx, user, runs[other], "memory_card", remember, false)
		for _, name := range []string{"routine_list", "assignment_list", "watch_list"} {
			h.call(t, ctx, user, runs[i], name, map[string]any{}, true)
		}
		h.assertRuntimeIsolation(t, user)
	}
	h.memoryRightFences(t, ctx, runs)
	ownerT := t
	t.Run("channel-memory-confirmation", func(t *testing.T) {
		h.memoryChannelConfirmation(t, ctx, runs, func() {
			stopToolHub()
			stopToolHub = h.startToolHub(ownerT, ctx)
		})
	})
	release()
	for range f.Users {
		m32Require(t, <-errors)
		if response := <-outcomes; response.Status != "completed" || response.Text != "M32 acceptance complete" {
			t.Fatal("actual Hermes run did not complete")
		}
	}
	for i, user := range f.Users {
		h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32"}, false)
	}
	stopToolHub()
	stopToolHub = h.startToolHub(t, ctx)
	for i, user := range f.Users {
		h.call(t, ctx, user, runs[i], "memory_search", map[string]any{"query": "M32"}, false)
		h.call(t, ctx, user, api.RunContext{}, "routine_list", map[string]any{}, true)
	}
	h.captureLostAcknowledgement(t, ctx, runs, [2]string{"M32 private recall " + f.Users[0], "M32 private recall " + f.Users[1]}, func() {
		stopToolHub()
		stopToolHub = h.startToolHub(t, ctx)
	})
	h.dueWorkRecovery(t, ctx, func() {
		stopToolHub()
		stopToolHub = h.startToolHub(t, ctx)
	})
	t.Log("passed: concurrent actual runs, per-user recall, foreign/wrong/missing/ended run denial, automation projections, ToolHub restart, runtime mounts")
}

type m32Lifecycle struct {
	f                                    devcheck.M32Fixture
	image, environment, control, toolURL string
	supervisor                           *httptest.Server
	manager                              *supervisor.Manager
	tokens                               map[string]string
	containers, networks                 []string
	gateway                              *communication.Gateway
	gatewayConfig                        communication.Config
	communicationOnline                  atomic.Bool
	channelSends                         <-chan int64
}

func newM32Lifecycle(t *testing.T, ctx context.Context, image, environment string, f devcheck.M32Fixture) *m32Lifecycle {
	t.Helper()
	h := &m32Lifecycle{f: f, image: image, environment: environment, control: strings.Repeat("m32-control-", 4), tokens: map[string]string{}}
	bind := "127.0.0.1:0"
	if image != "" {
		bind = "0.0.0.0:0"
	}
	listener, err := net.Listen("tcp", bind)
	m32Require(t, err)
	toolListen := listener.Addr().String()
	toolPort := listener.Addr().(*net.TCPAddr).Port
	h.toolURL = fmt.Sprintf("http://127.0.0.1:%d", toolPort)
	m32Require(t, listener.Close())
	registryPath, err := identityhub.StorePath(filepath.Join(f.Root, ".control"), environment)
	m32Require(t, err)
	registry, err := identityhub.Open(registryPath)
	m32Require(t, err)
	t.Cleanup(func() { m32Require(t, registry.Close()) })
	// Register cleanup before the first resource; each successful create is owned explicitly.
	t.Cleanup(func() {
		if image == "" {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		for _, user := range f.Users {
			for _, mode := range []string{"gateway", "gateway-2", "gateway-3", "gateway-4", "gateway-5"} {
				if h.manager == nil {
					continue
				}
				auth := f.Auth[user]
				rt, ok, err := h.manager.Status(supervisor.Binding{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, UserID: user, RuntimeMode: mode})
				if err != nil {
					t.Errorf("lookup owned M32 runtime for cleanup: %v", err)
				}
				if ok {
					h.containers = append(h.containers, rt.Container, rt.Container+"-ctl")
				}
			}
		}
		for i := len(h.containers) - 1; i >= 0; i-- {
			if err := exec.CommandContext(cleanup, "docker", "rm", "-f", h.containers[i]).Run(); err != nil {
				t.Errorf("remove owned M32 container: %v", err)
			}
		}
		for i := len(h.networks) - 1; i >= 0; i-- {
			if err := exec.CommandContext(cleanup, "docker", "network", "rm", h.networks[i]).Run(); err != nil {
				t.Errorf("remove owned M32 network: %v", err)
			}
		}
		// Runtime UID 10001 owns only this newly allocated fixture tree.
		if err := exec.CommandContext(cleanup, "docker", "run", "--rm", "--user", "0:0", "--network", "none", "-v", rootMount(f.Root, "/fixture"), "--entrypoint", "chmod", image, "-R", "a+rwX", "/fixture").Run(); err != nil {
			t.Errorf("make isolated M32 state removable: %v", err)
		}
	})
	controlNet := "hermes-m32-control-" + f.Users[0]
	if image != "" {
		h.docker(t, ctx, "network", "create", controlNet)
		h.networks = append(h.networks, controlNet)
	}
	command := func(ctx context.Context, args ...string) ([]byte, error) {
		if image == "" {
			return nil, fmt.Errorf("Docker is forbidden in startup-only acceptance")
		}
		for i, arg := range args {
			if arg == "hermes-hub-control" {
				args[i] = controlNet
			}
		}
		started := time.Now()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil && len(args) > 0 && args[0] == "run" {
			classes := []string{}
			for _, candidate := range []string{"port is already allocated", "address already in use", "container name", "permission denied", "no such file or directory", "invalid mount", "invalid volume", "network not found", "out of memory", "failed to create task", "mount", "bind", "not a directory", "file exists", "no space left", "invalid reference", "access is denied", "operation not permitted", "file sharing", "invalid argument", "failed to register", "no matching manifest", "runc create failed", "resource temporarily unavailable", "read-only file system", "context deadline exceeded"} {
				if strings.Contains(strings.ToLower(string(out)), candidate) {
					classes = append(classes, candidate)
				}
			}
			t.Logf("isolated Docker run failed classes=%v output_bytes=%d elapsed=%s deadline=%t cancelled=%t", classes, len(out), time.Since(started).Round(time.Second), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled))
		}
		return out, err
	}
	runtimeImage := image
	if runtimeImage == "" {
		runtimeImage = "m32-unspawned"
	}
	h.manager, err = supervisor.New(supervisor.Config{SpacesRoot: filepath.Join(f.Root, "spaces"), Image: runtimeImage, ScriptImage: os.Getenv("HUB_M32_SCRIPT_IMAGE"), Environment: environment, Network: controlNet, RuntimeAuth: h.control, WarmTTL: 30 * time.Minute, MaxConcurrent: 6, PortBase: 31000, Command: command, Identity: registry})
	m32Require(t, err)
	h.supervisor = httptest.NewServer(h.manager.Handler())
	t.Cleanup(h.supervisor.Close)
	passportRoot := filepath.Join(f.Root, "projected-passports")
	vaultRoot := filepath.Join(f.Root, "vaults")
	owners := filepath.Join(f.Root, "owners")
	users := []commodel.User{}
	var shared []identityhub.LegacySharedSpace
	for i, user := range f.Users {
		dir := filepath.Join(f.Root, "spaces", user)
		auth, err := stack.ReadSecrets(filepath.Join(dir, "runtime.auth"))
		m32Require(t, err)
		h.tokens[user] = auth["HUB_RUNTIME_AUTH"]
		m32Require(t, os.MkdirAll(filepath.Join(owners, user), 0700))
		m32Require(t, os.CopyFS(filepath.Join(passportRoot, user), os.DirFS(filepath.Join(dir, "generated"))))
		v := filepath.Join(vaultRoot, user)
		m32Require(t, vault.Ensure(v))
		m32Require(t, os.WriteFile(filepath.Join(v, "memory", "CORE.md"), []byte("M32-private-marker-"+user+"\n"), 0600))
		m32Require(t, os.WriteFile(filepath.Join(v, "memory", "cards", "notes", "m32-owner.md"), []byte("M32-private-marker-"+user+"\n"), 0600))
		routingUser := commodel.User{ID: user, RuntimeID: user, PolicyVersion: f.Auth[user].PolicyVersion, Enabled: true, TelegramIDs: []int64{int64(11 + i)}, Features: []string{"workspace", "telegram"}}
		if user == f.GroupSpace {
			shared = append(shared, identityhub.LegacySharedSpace{ID: user, Members: routingUser.TelegramIDs})
			routingUser.TelegramIDs = nil
		}
		users = append(users, routingUser)
		if image == "" {
			continue
		}
		// Only synthetic bind permissions change; authored/generated bytes stay intact.
		m32Require(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0777)
			}
			return nil
		}))
		agentNet := "hermes-hub-agent-" + user + "-" + environment
		h.docker(t, ctx, "network", "create", "--internal", agentNet)
		h.networks = append(h.networks, agentNet)
		for _, service := range []string{"toolhub", "model-relay", "egress-relay"} {
			name := "hermes-m32-" + service + "-" + user
			args := []string{"run", "-d", "--name", name, "--network", controlNet, "--user", fmt.Sprintf("10001:%d", max(0, os.Getgid())), "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--add-host", "toolhub-control:host-gateway", "--add-host", "communication-hub:host-gateway", "--add-host", "host.docker.internal:host-gateway", "--entrypoint", "hub-runtime"}
			if service == "toolhub" {
				args = append(args, "-e", fmt.Sprintf("HUB_TOOLHUB_RELAY_UPSTREAM=http://toolhub-control:%d", toolPort))
			}
			if service == "egress-relay" {
				args = append(args, "-e", "HUB_EGRESS_RELAY_HOSTS=communication-hub")
			}
			if service == "model-relay" {
				args = append(args, "--env-file", filepath.Join(dir, "model-relay.auth"), "-e", "HUB_MODEL_RELAY_REGISTRY=/models/models.yaml", "-e", "HUB_MODEL_RELAY_STATE=/model-state/model-select.json", "-e", "HUB_MODEL_RELAY_OWNER="+user, "-e", "HUB_ENVIRONMENT="+environment, "-e", fmt.Sprintf("HUB_TOOLHUB_URL=http://toolhub-control:%d", toolPort), "-e", "HUB_TOOLHUB_CONTROL="+h.control, "-v", rootMount(filepath.Join(dir, "generated", "model-registry", environment), "/models")+":ro", "-v", rootMount(filepath.Join(dir, "managed", environment, "model-relay"), "/model-state"))
			}
			entry := service
			if entry == "toolhub" {
				entry = "toolhub-relay"
			}
			args = append(args, os.Getenv("HUB_M32_SCRIPT_IMAGE"), entry)
			h.containers = append(h.containers, name)
			h.docker(t, ctx, args...)
			h.docker(t, ctx, "network", "connect", "--alias", service, agentNet, name)
		}
	}
	_, err = registry.ImportLegacyConfiguration(ctx, identityhub.LegacyConfiguration{Name: "m32", Organization: "personal", Users: users, SharedSpaces: shared})
	m32Require(t, err)
	routing := filepath.Join(f.Root, "routing.json")
	body, err := json.Marshal(map[string]any{"organization_id": "personal", "users": users})
	m32Require(t, err)
	m32Require(t, os.WriteFile(routing, body, 0600))
	channelURL, sends := m32ChannelFixture(t)
	h.channelSends = sends
	h.gatewayConfig = communication.Config{Supervised: true, Environment: environment, OrganizationID: "personal", Users: users, SpoolDir: filepath.Join(f.Root, "communication"), TelegramToken: "synthetic-m32-bot", APIBaseURL: channelURL, PollTimeout: time.Second, ControlAuth: h.tokens[f.Users[0]], ControlPrincipal: f.Users[0], AutomationAuth: h.control, ControlTokensFile: filepath.Join(f.Infra, "toolhub-tokens.json"), RuntimeURL: h.supervisor.URL, RuntimeAuth: h.control, ToolHubURL: h.toolURL}
	h.gatewayConfig.Identity = registry
	gateway, err := communication.New(h.gatewayConfig)
	m32Require(t, err)
	h.gateway = gateway
	h.communicationOnline.Store(true)
	commHandler := gateway.Handler()
	comm := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.communicationOnline.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		commHandler.ServeHTTP(w, r)
	}))
	if image != "" {
		comm.Listener.Close()
		comm.Listener, err = net.Listen("tcp", "0.0.0.0:8081")
		m32Require(t, err)
	}
	comm.Start()
	t.Cleanup(comm.Close)
	auth := f.Auth[f.Users[0]]
	for key, value := range map[string]string{
		"HUB_RUNTIME_AUTH": h.tokens[f.Users[0]], "HUB_TOOLHUB_TOKENS_FILE": filepath.Join(f.Infra, "toolhub-tokens.json"), "HUB_TOOLHUB_STORE": f.StorePath,
		"HUB_USER_ID": auth.PrincipalID, "HUB_PRINCIPAL_ID": auth.PrincipalID, "HUB_CONTEXT_ID": auth.ContextID, "HUB_RUNTIME_ID": auth.RuntimeID, "HUB_POLICY_VERSION": auth.PolicyVersion,
		"HUB_CAPABILITY_PROFILE_ID": auth.CapabilityProfile, "HUB_CAPABILITY_ENVIRONMENT": environment, "HUB_CAPABILITY_GENERATION": "1",
		"HUB_TOOLHUB_LISTEN": toolListen, "HUB_AGENT_EXEC_MODE": "supervisor", "HUB_PASSPORTS_BY_PRINCIPAL": passportRoot, "HUB_VAULTS_BY_PRINCIPAL": vaultRoot,
		"HUB_TOOL_GOVERNANCE": filepath.Join(filepath.Dir(f.StorePath), "governance.json"), "HUB_AUDIT_LEDGER": filepath.Join(f.Root, "audit.jsonl"),
		"HUB_COMMUNICATION_AUTH": h.control, "HUB_SUPERVISOR_AUTH": h.control, "HUB_RUNTIME_SUPERVISOR_URL": h.supervisor.URL, "HUB_TOOLHUB_URL_PATTERN": h.toolURL,
		"HUB_AUTOMATION_ROOT": filepath.Join(f.Root, "automation"), "HUB_AUTOMATION_OWNERS": owners, "HUB_AUTOMATION_LEGACY_SPOOL": filepath.Join(f.Root, "communication"), "HUB_AUTOMATION_ROUTING": routing,
		"HUB_AUTOMATION_ENVIRONMENT": environment, "HUB_AUTOMATION_JOB_URL": h.supervisor.URL, "HUB_COMMUNICATION_CONTROL_URL": comm.URL,
		"HUB_IDENTITY_URL": h.supervisor.URL, "HUB_IDENTITY_AUTH": h.control,
	} {
		t.Setenv(key, value)
	}
	return h
}

func rootMount(source, target string) string { return filepath.ToSlash(source) + ":" + target }

func (h *m32Lifecycle) docker(t *testing.T, ctx context.Context, args ...string) []byte {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		t.Fatalf("M32 docker %s failed: %v", args[0], err)
	}
	return out
}

func (h *m32Lifecycle) startToolHub(t *testing.T, ctx context.Context) func() {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- toolhub.Serve(runCtx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("ToolHub shutdown: %v", err)
				}
			case <-time.After(15 * time.Second):
				t.Error("ToolHub did not stop")
			}
		})
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			cancel()
			t.Fatalf("ToolHub startup: %v", err)
		default:
		}
		if c, err := net.DialTimeout("tcp", strings.TrimPrefix(h.toolURL, "http://"), 100*time.Millisecond); err == nil {
			c.Close()
			return stop
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			cancel()
			t.Fatal("ToolHub startup cancelled")
		}
	}
	cancel()
	t.Fatal("ToolHub listener did not become ready")
	return stop
}

func (h *m32Lifecycle) execute(ctx context.Context, job runtime.ExecuteRequest) (runtime.ExecuteResponse, error) {
	body, err := json.Marshal(job)
	if err != nil {
		return runtime.ExecuteResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", h.supervisor.URL+"/v1/jobs", strings.NewReader(string(body)))
	if err != nil {
		return runtime.ExecuteResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+h.control)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", runtime.RunStreamContentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return runtime.ExecuteResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return runtime.ExecuteResponse{}, fmt.Errorf("supervisor dispatch HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	var last runtime.ExecuteResponse
	for {
		var event runtime.ExecuteResponse
		if err := decoder.Decode(&event); err != nil {
			return last, err
		}
		last = event
		if commodel.TerminalStatus(event.Status) {
			return event, nil
		}
	}
}

func (h *m32Lifecycle) waitRun(t *testing.T, ctx context.Context, id string) api.RunContext {
	t.Helper()
	authority := task.NewHTTPClient(h.supervisor.URL, h.control, nil)
	for range 100 {
		run, err := authority.Run(ctx, id)
		if err == nil && run.RunID != "" {
			return run
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("run lookup cancelled")
		}
	}
	t.Fatal("real run ID not persisted")
	return api.RunContext{}
}

type m32Bearer struct {
	token string
	run   api.RunContext
}

func (b m32Bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	r.Header.Set(api.HeaderJobID, b.run.JobID)
	r.Header.Set(api.HeaderRunID, b.run.RunID)
	return http.DefaultTransport.RoundTrip(r)
}

func (h *m32Lifecycle) call(t *testing.T, ctx context.Context, user string, run api.RunContext, tool string, args map[string]any, want bool) string {
	t.Helper()
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	session := h.session(t, callCtx, user, run)
	defer session.Close()
	return m32SessionCall(t, callCtx, session, tool, args, want)
}

func (h *m32Lifecycle) session(t *testing.T, ctx context.Context, user string, run api.RunContext) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "m32-acceptance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.toolURL + "/mcp", HTTPClient: &http.Client{Transport: m32Bearer{h.tokens[user], run}}, DisableStandaloneSSE: true}, nil)
	m32Require(t, err)
	return session
}

func m32SessionCall(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any, want bool) string {
	return m32SessionCallWithRun(t, ctx, session, tool, args, "", want)
}

func m32SessionCallWithRun(t *testing.T, ctx context.Context, session *mcp.ClientSession, tool string, args map[string]any, runID string, want bool) string {
	t.Helper()
	var meta mcp.Meta
	if runID != "" {
		meta = mcp.Meta{"x-hermes-hub-run-id": runID}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Meta: meta, Name: "tool_call", Arguments: map[string]any{"tool": tool, "arguments": args}})
	ok := err == nil && result != nil && !result.IsError
	if ok != want {
		t.Fatalf("%s admission=%t expected=%t err=%v", tool, ok, want, err)
	}
	if !ok {
		return ""
	}
	data, err := json.Marshal(result)
	m32Require(t, err)
	return string(data)
}

func (h *m32Lifecycle) assertRuntimeIsolation(t *testing.T, user string) {
	t.Helper()
	rt, ok, err := h.runtimeStatus(user)
	m32Require(t, err)
	if !ok || !rt.Managed {
		t.Fatal("actual managed runtime missing")
	}
	data := h.docker(t, t.Context(), "inspect", "--format", "{{json .Mounts}}", rt.Container)
	var mounts []struct{ Destination string }
	m32Require(t, json.Unmarshal(data, &mounts))
	for _, mount := range mounts {
		for _, forbidden := range []string{"/vault", "/src", "/spaces"} {
			if mount.Destination == forbidden || strings.HasPrefix(mount.Destination, forbidden+"/") {
				t.Fatal("runtime has forbidden memory/source/space mount")
			}
		}
	}
	containerUser := strings.TrimSpace(string(h.docker(t, t.Context(), "inspect", "--format", "{{.Config.User}}", rt.Container)))
	if containerUser != fmt.Sprintf("10001:%d", max(0, os.Getgid())) {
		t.Fatal("runtime lost its non-root UID and controller group contract")
	}
	h.docker(t, t.Context(), "exec", rt.Container, "sh", "-ec", `for file in /config/stack.yaml /config/config.yaml /config/models.yaml /config/workspace.yaml /config/tools.yaml /config/grants.yaml /config/revision /config/tool-policy.json /config/tool-manifest.json; do test -r "$file"; done`)
}

func (h *m32Lifecycle) runtimeStatus(user string) (supervisor.Runtime, bool, error) {
	auth := h.f.Auth[user]
	return h.manager.Status(supervisor.Binding{PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, UserID: user, RuntimeMode: "gateway"})
}

func m32Require(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func m32HeldProvider(t *testing.T, inspect func(json.RawMessage), reply ...func(http.ResponseWriter, bool, json.RawMessage) bool) (*httptest.Server, func(), <-chan struct{}) {
	t.Helper()
	gate, entered := make(chan struct{}), make(chan struct{}, 32)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	m32Require(t, err)
	provider := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		inspect(request.Messages)
		select {
		case <-gate:
		default:
			select {
			case entered <- struct{}{}:
			case <-r.Context().Done():
				return
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if len(reply) != 0 && reply[0](w, request.Stream, request.Messages) {
			return
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"m32\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"M32 acceptance complete\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"m32","object":"chat.completion","model":"m32-fixture","choices":[{"index":0,"message":{"role":"assistant","content":"M32 acceptance complete"},"finish_reason":"stop"}]}`)
		}
	}))
	provider.Listener = listener
	provider.Start()
	t.Cleanup(func() { release(); provider.CloseClientConnections(); provider.Close() })
	return provider, release, entered
}
