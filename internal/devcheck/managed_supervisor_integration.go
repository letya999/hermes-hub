//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/communication"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/passport"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ManagedSupervisorCanary proves the supervisor-managed lifecycle end to end
// on real Docker: the runtime spawns only on the internal per-user agent
// network, a dual-homed control relay carries the host-side control route on
// the loopback, the in-container isolation verifier passes with the reviewed
// relay topology, and teardown removes runtime and sidecar together.
func ManagedSupervisorCanary(ctx context.Context, image string) (result error) {
	if strings.TrimSpace(image) == "" {
		return errors.New("managed supervisor canary image is required")
	}
	id := make([]byte, 4)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	suffix := hex.EncodeToString(id)
	userID := "mg" + suffix
	env := "prod"
	containerRuntime := strings.TrimSpace(os.Getenv("HUB_CONTAINER_RUNTIME"))
	if containerRuntime == "" {
		containerRuntime = "runc"
	}
	agentNet := "hermes-hub-agent-" + userID + "-" + env
	sharedNet, controlNet := "hermes-mgsup-runtime-"+suffix, "hermes-mgsup-control-"+suffix
	containers, ownedNetworks := []string{}, []string{}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for i := len(containers) - 1; i >= 0; i-- {
			if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", containers[i]).CombinedOutput(); err != nil && !strings.Contains(string(out), "No such container") {
				result = errors.Join(result, fmt.Errorf("remove canary container %s: %w: %s", containers[i], err, out))
			}
		}
		for i := len(ownedNetworks) - 1; i >= 0; i-- {
			if out, err := exec.CommandContext(cleanup, "docker", "network", "rm", ownedNetworks[i]).CombinedOutput(); err != nil {
				result = errors.Join(result, fmt.Errorf("remove canary network %s: %w: %s", ownedNetworks[i], err, out))
			}
		}
	}()
	docker := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker %s: %w: %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := docker("network", "create", "--internal", agentNet); err != nil {
		return err
	}
	ownedNetworks = append(ownedNetworks, agentNet)
	for _, network := range []string{sharedNet, controlNet} {
		if _, err := docker("network", "create", network); err != nil {
			return err
		}
		ownedNetworks = append(ownedNetworks, network)
	}
	const toolhubFixture = `import json
from http.server import BaseHTTPRequestHandler,HTTPServer
class Handler(BaseHTTPRequestHandler):
 def do_POST(self):
  l=int(self.headers.get('Content-Length') or 0)
  try: req=json.loads(self.rfile.read(l) or b'{}')
  except Exception: req={}
  method,rid=req.get('method',''),req.get('id')
  if method=='initialize': res={"protocolVersion":"2025-03-26","capabilities":{"tools":{}},"serverInfo":{"name":"toolhub-stub","version":"0.1"}}
  elif method=='tools/list': res={"tools":[]}
  elif method=='ping': res={}
  elif method.startswith('notifications/'): self.send_response(202); self.end_headers(); return
  else: res={}
  body=json.dumps({"jsonrpc":"2.0","id":rid,"result":res}).encode()
  self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(body)
 def do_GET(self):
  self.send_response(200); self.end_headers(); self.wfile.write(b'mcp-ok')
 def do_DELETE(self): self.do_GET()
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8090),Handler).serve_forever()`
	toolhubStub := "hermes-mgsup-toolhub-" + suffix
	if _, err := docker("run", "-d", "--name", toolhubStub, "--network", sharedNet, "--network-alias", "toolhub-control", "--entrypoint", "python", image, "-c", toolhubFixture); err != nil {
		return err
	}
	containers = append(containers, toolhubStub)
	const cliproxyFixture = `import json
from http.server import BaseHTTPRequestHandler,HTTPServer
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  body=json.dumps({"object":"list","data":[{"id":"synthetic","object":"model"}]}).encode()
  self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(body)
 def do_POST(self):
  request=self.rfile.read(int(self.headers.get('Content-Length') or 0))
  if b'"stream":true' in request or b'"stream": true' in request:
   body=b'data: {"id":"chatcmpl-canary","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"canary"},"finish_reason":null}]}\n\ndata: {"id":"chatcmpl-canary","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n'
   self.send_response(200); self.send_header('Content-Type','text/event-stream'); self.end_headers(); self.wfile.write(body); return
  body=json.dumps({"id":"chatcmpl-canary","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"canary"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}).encode()
  self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers(); self.wfile.write(body)
 def log_message(self,*args): pass
HTTPServer(('0.0.0.0',8317),Handler).serve_forever()`
	cliproxyStub := "hermes-mgsup-cliproxy-" + suffix
	if _, err := docker("run", "-d", "--name", cliproxyStub, "--network", sharedNet, "--network-alias", "cliproxy", "--entrypoint", "python", image, "-c", cliproxyFixture); err != nil {
		return err
	}
	containers = append(containers, cliproxyStub)
	// The compose-owned agent-facing relays: identical supervision contract to
	// the rendered managed topology (relay joins agent network plus the shared
	// control plane; only the narrow alias resolves inside).
	relays := []struct{ name, alias, command, env string }{
		{"hermes-mgsup-toolrelay-" + suffix, "toolhub", "toolhub-relay", ""},
		{"hermes-mgsup-modelrelay-" + suffix, "model-relay", "model-relay", "HUB_MODEL_RELAY_MODEL=synthetic"},
		{"hermes-mgsup-egressrelay-" + suffix, "egress-relay", "egress-relay", "HUB_EGRESS_RELAY_CONNECT_TARGET=fixture.invalid:22"},
	}
	for _, relay := range relays {
		args := []string{"run", "-d", "--name", relay.name, "--network", agentNet, "--network-alias", relay.alias, "--entrypoint", "hub-runtime"}
		if relay.env != "" {
			args = append(args, "-e", relay.env)
		}
		args = append(args, image, relay.command)
		if _, err := docker(args...); err != nil {
			return err
		}
		containers = append(containers, relay.name)
		if _, err := docker("network", "connect", sharedNet, relay.name); err != nil {
			return err
		}
	}
	// Wait until the agent-network aliases actually accept connections: the
	// in-container verifier dials toolhub:8090 and model-relay:8318 once.
	wait := `import socket,sys,time
for attempt in range(60):
 try:
		  socket.create_connection(("toolhub",8090),1).close(); socket.create_connection(("model-relay",8318),1).close(); socket.create_connection(("egress-relay",8319),1).close(); sys.exit(0)
 except OSError: time.sleep(1)
sys.exit(1)`
	if _, err := docker("run", "--rm", "--network", agentNet, "--entrypoint", "python", image, "-c", wait); err != nil {
		return fmt.Errorf("managed relays never became reachable on the agent network: %w", err)
	}
	// Managed context fixtures: reviewed settings, supervisor selection,
	// zero-template source config and the managed env file.
	root, err := os.MkdirTemp("", "hermes-mgsup-")
	if err != nil {
		return err
	}
	defer func() {
		// The managed runtime writes its state as container uid 10001: on
		// Linux the host user cannot unlink those files directly.
		if err := removeCanaryTree(ctx, image, root); err != nil {
			result = errors.Join(result, fmt.Errorf("remove supervisor canary root: %w", err))
		}
	}()
	contextRoot, err := managedCanaryContext(root, userID, env)
	if err != nil {
		return err
	}
	cfg := supervisor.Config{SpacesRoot: root, Image: image, RuntimeAuth: "supervisor-control-0123456789abcdef", Network: sharedNet, WarmTTL: time.Second, PortBase: 28600, ContainerRuntime: containerRuntime}
	cfg.Command = func(callCtx context.Context, args ...string) ([]byte, error) {
		// Namespace the fixed control bridge inside this fixture, preserving real Docker operations.
		if len(args) != 0 && args[0] == "network" {
			for i, arg := range args {
				if arg == "hermes-hub-control" {
					args[i] = controlNet
				}
			}
		}
		output, err := exec.CommandContext(callCtx, "docker", args...).CombinedOutput()
		return output, err
	}
	m, err := supervisor.New(cfg)
	if err != nil {
		return err
	}
	binding := supervisor.Binding{PrincipalID: userID, ContextID: userID, RuntimeID: userID, RuntimeMode: "gateway", UserID: userID, OrganizationID: "personal", PolicyVersion: "canary", ContextRoot: contextRoot}
	// The real channel path owns the cold spawn: a mock message traverses the
	// gateway's durable spool and dispatch loops, and the supervisor's /v1/jobs
	// admission spawns the runtime under the selected sandbox inside that
	// request — no direct Ensure shortcut.
	if err := managedMockChannelGate(ctx, m, cfg.RuntimeAuth, root, userID); err != nil {
		return fmt.Errorf("mock channel admission: %w", err)
	}
	runtime, err := m.Ensure(ctx, binding)
	if err != nil {
		return fmt.Errorf("managed supervised spawn: %w", err)
	}
	containers = append(containers, runtime.Container, runtime.Container+"-ctl")
	if !runtime.Managed || runtime.State != supervisor.Busy {
		return fmt.Errorf("managed runtime not busy: %+v", runtime)
	}
	// The real ToolHub admission and private executor must reach the reviewed
	// connection config in the supervisor-spawned container. No tunnel opens.
	store := toolhub.NewStore()
	definition := agenttools.HubToolsDefinition()
	if err := store.RegisterDefinition(definition); err != nil {
		return err
	}
	caller := identity.Envelope{Schema: identity.Schema, PrincipalID: userID, ExternalIdentityID: userID,
		ContextID: userID, RuntimeID: userID, ConversationID: "canary", DeliveryTargetID: "canary", PolicyVersion: "canary"}
	if _, err := store.Enable(caller, definition.DefinitionID, definition.Version); err != nil {
		return err
	}
	var admitted, completed bool
	gateway := &toolhub.Gateway{Store: store, Backend: api.AgentExecBackend{Exec: api.DockerAgentExec([]string{"docker"}, api.ManagedAgentContainer)},
		AuditWrite: func(event string, fields map[string]string) error {
			admitted = admitted || event == "admit"
			completed = completed || event == "allow" && fields["outcome"] == "ok"
			return nil
		}}
	name := api.ProjectedToolName(definition.DefinitionID, definition.Version, "exposure_list")
	call, err := gateway.CallAuthorized(ctx, caller, name, map[string]any{})
	if err != nil || call == nil || call.IsError || !admitted || !completed {
		return fmt.Errorf("managed exposure ToolHub read failed: admitted=%t completed=%t err=%v", admitted, completed, err)
	}
	var listed struct {
		Declared []struct {
			Name string `json:"name"`
		} `json:"declared_endpoints"`
		Tunnels []any `json:"tunnels"`
	}
	decoded := false
	if call.StructuredContent != nil {
		body, marshalErr := json.Marshal(call.StructuredContent)
		decoded = marshalErr == nil && json.Unmarshal(body, &listed) == nil
	}
	for _, item := range call.Content {
		if content, ok := item.(*mcp.TextContent); ok && !decoded && json.Unmarshal([]byte(content.Text), &listed) == nil {
			decoded = true
		}
	}
	if !decoded || len(listed.Declared) != 1 || listed.Declared[0].Name != "canary" || len(listed.Tunnels) != 0 {
		return errors.New("managed exposure ToolHub read returned unexpected declared endpoints or live tunnels")
	}
	inspect := func(container string) (map[string]any, error) {
		out, err := docker("inspect", container)
		if err != nil {
			return nil, err
		}
		var decoded []map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil || len(decoded) != 1 {
			return nil, fmt.Errorf("inspect %s unparseable: %w", container, err)
		}
		return decoded[0], nil
	}
	runtimeInspect, err := inspect(runtime.Container)
	if err != nil {
		return err
	}
	hostConfig, ok := runtimeInspect["HostConfig"].(map[string]any)
	if !ok || hostConfig["Runtime"] != containerRuntime {
		return fmt.Errorf("managed runtime isolation mismatch: %v", hostConfig["Runtime"])
	}
	if containerRuntime != "runc" {
		hosts := map[string]bool{}
		extraHosts, _ := hostConfig["ExtraHosts"].([]any)
		for _, raw := range extraHosts {
			entry := fmt.Sprint(raw)
			if strings.HasSuffix(entry, ":host-gateway") || strings.Contains(entry, "host.docker.internal:") {
				return fmt.Errorf("sandbox runtime gained a host gateway: %s", entry)
			}
			name, _, found := strings.Cut(entry, ":")
			if found {
				hosts[name] = true
			}
		}
		for _, required := range []string{"toolhub", "model-relay", "egress-relay"} {
			if !hosts[required] {
				return fmt.Errorf("sandbox runtime is missing private relay alias %s", required)
			}
		}
	}
	networks := map[string]any{}
	if data, ok := runtimeInspect["NetworkSettings"].(map[string]any); ok {
		if n, ok := data["Networks"].(map[string]any); ok {
			networks = n
		}
	}
	if len(networks) != 1 || networks[agentNet] == nil {
		return fmt.Errorf("managed runtime joined unexpected networks: %v", networks)
	}
	for _, mount := range runtimeInspect["Mounts"].([]any) {
		entry := mount.(map[string]any)
		for _, denied := range []string{"/scope", "/org", "/archive", "/opt/hub/skills", "/run/broker-secrets"} {
			if entry["Destination"] == denied {
				return fmt.Errorf("managed runtime kept denied mount %s", denied)
			}
		}
	}
	relayInspect, err := inspect(runtime.Container + "-ctl")
	if err != nil {
		return err
	}
	relayHost, ok := relayInspect["HostConfig"].(map[string]any)
	if !ok || relayHost["Runtime"] != "runc" {
		return fmt.Errorf("control relay isolation drifted: %v", relayHost["Runtime"])
	}
	relayNets := relayInspect["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
	if relayNets[agentNet] == nil || relayNets[controlNet] == nil {
		return fmt.Errorf("control relay is not dual-homed: %v", relayNets)
	}
	port := strings.TrimPrefix(runtime.Address, "http://127.0.0.1:")
	bindings := relayInspect["HostConfig"].(map[string]any)["PortBindings"].(map[string]any)
	if len(bindings) != 1 || bindings["8091/tcp"] == nil || !strings.Contains(fmt.Sprint(bindings["8091/tcp"]), "127.0.0.1") || !strings.Contains(fmt.Sprint(bindings["8091/tcp"]), port) {
		return fmt.Errorf("control relay publishes more than the loopback port: %v", bindings)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, runtime.Address+"/readyz", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer managed-canary-auth-0123456789abcdef")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("control route through relay unreachable: %w", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("control route through relay not ready: %d", response.StatusCode)
	}
	unauth, err := http.NewRequestWithContext(ctx, http.MethodGet, runtime.Address+"/readyz", nil)
	if err != nil {
		return err
	}
	unauthResponse, err := http.DefaultClient.Do(unauth)
	if err == nil {
		_ = unauthResponse.Body.Close()
		if unauthResponse.StatusCode == http.StatusOK {
			return fmt.Errorf("control relay exposed an unauthenticated runtime route")
		}
	}
	execution := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope(userID, 11, userID, "canary"), JobID: "sandbox-managed-" + suffix, OrganizationID: "personal", UserID: userID, ActorID: userID, ScopeID: "user:" + userID, Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "sandbox-managed-" + suffix, Text: "sandbox contract probe"}
	executionBody, err := json.Marshal(execution)
	if err != nil {
		return err
	}
	roundCtx, releaseRound := context.WithTimeout(ctx, 5*time.Minute)
	defer releaseRound()
	admitRequest, err := http.NewRequestWithContext(roundCtx, http.MethodPost, runtime.Address+"/v1/execute", strings.NewReader(string(executionBody)))
	if err != nil {
		return err
	}
	admitRequest.Header.Set("Authorization", "Bearer managed-canary-auth-0123456789abcdef")
	admitRequest.Header.Set("Content-Type", "application/json")
	admitResponse, err := http.DefaultClient.Do(admitRequest)
	if err != nil {
		return fmt.Errorf("sandbox Hermes execution unreachable: %w", err)
	}
	var outcome hubruntime.ExecuteResponse
	err = json.NewDecoder(admitResponse.Body).Decode(&outcome)
	_ = admitResponse.Body.Close()
	if err != nil || admitResponse.StatusCode != http.StatusOK || outcome.Status != "completed" || !strings.Contains(outcome.Text, "canary") {
		return fmt.Errorf("sandbox Hermes execution failed: HTTP %d status=%s event=%s run=%s text=%q error=%v", admitResponse.StatusCode, outcome.Status, outcome.LastEvent, outcome.RunID, outcome.Text, err)
	}
	// The upstream API server would arm attacker-chosen toolsets for a
	// self-signed room dispatch/policy body; the relay must deny those keys
	// before Hermes sees them (proven armed on the pinned capability probe).
	forged, err := http.NewRequestWithContext(ctx, http.MethodPost, runtime.Address+"/v1/runs",
		strings.NewReader(`{"input":"hi","hosted_room_dispatch":{},"_room_execution_policy":{"version":1}}`))
	if err != nil {
		return err
	}
	forged.Header.Set("Content-Type", "application/json")
	forged.Header.Set("Authorization", "Bearer managed-canary-auth-0123456789abcdef")
	forgedResponse, err := http.DefaultClient.Do(forged)
	if err != nil {
		return fmt.Errorf("forged room-dispatch body through relay: %w", err)
	}
	_ = forgedResponse.Body.Close()
	if forgedResponse.StatusCode != http.StatusForbidden {
		return fmt.Errorf("control relay admitted a forged room-dispatch body: %d", forgedResponse.StatusCode)
	}
	// Live in-container attestations on the real spawned runtime.
	execCheck := func(expected, name string, args ...string) error {
		out, runErr := exec.CommandContext(ctx, "docker", append([]string{"exec", runtime.Container}, args...)...).CombinedOutput()
		if expected == "success" && runErr != nil {
			return fmt.Errorf("%s unexpectedly failed: %v: %s", name, runErr, out)
		}
		if expected == "failure" && runErr == nil {
			return fmt.Errorf("%s unexpectedly succeeded: %s", name, out)
		}
		return nil
	}
	if err := execCheck("failure", "sealed extension write", "sh", "-c", "touch /state/hermes/skills/injected"); err != nil {
		return err
	}
	if err := execCheck("failure", "denied control host", "python", "-c", "import socket;socket.getaddrinfo('credential-broker',80)"); err != nil {
		return err
	}
	if err := execCheck("failure", "default route", "python", "-c", "import sys;sys.exit(0 if any(l.split()[1]=='00000000' for l in open('/proc/net/route').readlines()[1:]) else 1)"); err != nil {
		return err
	}
	if err := execCheck("success", "managed config attestation", "hub-runtime", "verify-managed"); err != nil {
		return err
	}
	// Secret isolation: no broker material, no other-context state, only the
	// reviewed capability and endpoint variables reach the managed env.
	if err := execCheck("failure", "broker secrets mount", "test", "-d", "/run/broker-secrets"); err != nil {
		return err
	}
	if err := execCheck("failure", "context scope mount", "test", "-d", "/scope"); err != nil {
		return err
	}
	if err := execCheck("success", "workspace write", "sh", "-c", "printf sandbox-runtime > /workspace/managed-sandbox-marker"); err != nil {
		return err
	}
	envOut, envErr := exec.CommandContext(ctx, "docker", "exec", runtime.Container, "env").CombinedOutput()
	if envErr != nil {
		return fmt.Errorf("managed env scan failed: %w", envErr)
	}
	for _, line := range strings.Split(string(envOut), "\n") {
		key := strings.SplitN(line, "=", 2)[0]
		if strings.Contains(key, "BROKER") || strings.Contains(key, "CREDENTIAL") || strings.Contains(key, "TOKEN") || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GH_") {
			return errors.New("managed runtime env includes a protected control-plane variable")
		}
	}
	// Link-local cloud metadata is unreachable without a default route.
	if err := execCheck("failure", "metadata endpoint", "python", "-c", "import socket;socket.create_connection(('169.254.169.254',80),2)"); err != nil {
		return err
	}
	// Mount immutability across a container restart: same mounts, same
	// effective config, attestations still pass.
	configHash := func() (string, error) {
		out, hashErr := exec.CommandContext(ctx, "docker", "exec", runtime.Container, "sha256sum", "/state/hermes/config.yaml").CombinedOutput()
		if hashErr != nil {
			return "", fmt.Errorf("config hash unavailable: %v: %s", hashErr, out)
		}
		return strings.Fields(string(out))[0], nil
	}
	beforeHash, err := configHash()
	if err != nil {
		return err
	}
	mountSignature := func(decoded map[string]any) string {
		entries := []string{}
		if mounts, ok := decoded["Mounts"].([]any); ok {
			for _, mount := range mounts {
				entry, _ := mount.(map[string]any)
				entries = append(entries, fmt.Sprintf("%s|%s|%s|%v", entry["Destination"], entry["Type"], entry["Source"], entry["RW"]))
			}
		}
		if host, ok := decoded["HostConfig"].(map[string]any); ok {
			if tmpfs, ok := host["Tmpfs"].(map[string]any); ok {
				for destination, spec := range tmpfs {
					entries = append(entries, fmt.Sprintf("tmpfs|%s|%s", destination, spec))
				}
			}
			if binds, ok := host["Binds"].([]any); ok {
				for _, bind := range binds {
					entries = append(entries, fmt.Sprintf("bind|%s", bind))
				}
			}
		}
		sort.Strings(entries)
		return strings.Join(entries, "\n")
	}
	mountsBefore := mountSignature(runtimeInspect)
	if _, err := docker("restart", runtime.Container); err != nil {
		return fmt.Errorf("managed runtime restart: %w", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, runtime.Address+"/readyz", nil)
		request.Header.Set("Authorization", "Bearer managed-canary-auth-0123456789abcdef")
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			return errors.New("managed runtime never came back ready after restart")
		}
		time.Sleep(time.Second)
	}
	restartedInspect, err := inspect(runtime.Container)
	if err != nil {
		return err
	}
	if mountsBefore != mountSignature(restartedInspect) {
		return errors.New("managed runtime mounts changed across restart")
	}
	afterHash, err := configHash()
	if err != nil {
		return err
	}
	if beforeHash != afterHash {
		return errors.New("managed effective config changed across restart")
	}
	if err := execCheck("success", "managed config attestation after restart", "hub-runtime", "verify-managed"); err != nil {
		return err
	}
	if err := execCheck("failure", "sealed extension write after restart", "sh", "-c", "touch /state/hermes/hooks/injected"); err != nil {
		return err
	}
	// Secondary-runtime parity: a second managed context gets its own internal
	// network, its own relay and a distinct loopback port; neither runtime can
	// name-resolve the other.
	user2 := "mh" + suffix
	agentNet2 := "hermes-hub-agent-" + user2 + "-" + env
	if _, err := docker("network", "create", "--internal", agentNet2); err != nil {
		return err
	}
	ownedNetworks = append(ownedNetworks, agentNet2)
	for _, relay := range []struct{ name, alias string }{
		{"hermes-mgsup-toolrelay-" + suffix, "toolhub"},
		{"hermes-mgsup-modelrelay-" + suffix, "model-relay"},
		{"hermes-mgsup-egressrelay-" + suffix, "egress-relay"},
	} {
		if _, err := docker("network", "connect", "--alias", relay.alias, agentNet2, relay.name); err != nil {
			return fmt.Errorf("attach relay to second agent network: %w", err)
		}
	}
	contextRoot2, err := managedCanaryContext(root, user2, env)
	if err != nil {
		return err
	}
	binding2 := supervisor.Binding{PrincipalID: user2, ContextID: user2, RuntimeID: user2, RuntimeMode: "gateway", UserID: user2, OrganizationID: "personal", PolicyVersion: "canary", ContextRoot: contextRoot2}
	runtime2, err := m.Ensure(ctx, binding2)
	if err != nil {
		return fmt.Errorf("secondary managed supervised spawn: %w", err)
	}
	containers = append(containers, runtime2.Container, runtime2.Container+"-ctl")
	if runtime.Address == runtime2.Address {
		return fmt.Errorf("two managed runtimes share loopback address %s", runtime.Address)
	}
	runtime2Inspect, err := inspect(runtime2.Container)
	if err != nil {
		return err
	}
	hostConfig2, ok := runtime2Inspect["HostConfig"].(map[string]any)
	if !ok || hostConfig2["Runtime"] != containerRuntime {
		return fmt.Errorf("secondary managed runtime isolation mismatch: %v", hostConfig2["Runtime"])
	}
	networks2 := runtime2Inspect["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
	if len(networks2) != 1 || networks2[agentNet2] == nil {
		return fmt.Errorf("secondary runtime joined unexpected networks: %v", networks2)
	}
	relay2Inspect, err := inspect(runtime2.Container + "-ctl")
	if err != nil {
		return err
	}
	relay2Host, ok := relay2Inspect["HostConfig"].(map[string]any)
	if !ok || relay2Host["Runtime"] != "runc" {
		return fmt.Errorf("secondary control relay isolation drifted: %v", relay2Host["Runtime"])
	}
	relay2Nets := relay2Inspect["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
	if relay2Nets[agentNet2] == nil || relay2Nets[controlNet] == nil {
		return fmt.Errorf("secondary control relay is not dual-homed: %v", relay2Nets)
	}
	if err := execCheck("failure", "sibling runtime resolution", "python", "-c", "import socket;socket.getaddrinfo('"+runtime2.Container+"',8080)"); err != nil {
		return err
	}
	// Generation rollover: reaping and respawning the first context must
	// produce a fresh runtime plus relay without leftover names or ports.
	if err := m.ReleaseBinding(binding); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	if _, err := inspect(runtime.Container); err == nil {
		return fmt.Errorf("managed runtime container survived teardown")
	}
	if _, err := inspect(runtime.Container + "-ctl"); err == nil {
		return fmt.Errorf("control relay survived teardown")
	}
	runtime3, err := m.Ensure(ctx, binding)
	if err != nil {
		return fmt.Errorf("managed respawn after reap: %w", err)
	}
	containers = append(containers, runtime3.Container, runtime3.Container+"-ctl")
	if runtime3.Generation == runtime.Generation {
		return errors.New("managed respawn did not advance generation")
	}
	runtime3Inspect, err := inspect(runtime3.Container)
	if err != nil {
		return err
	}
	hostConfig3, ok := runtime3Inspect["HostConfig"].(map[string]any)
	if !ok || hostConfig3["Runtime"] != containerRuntime {
		return fmt.Errorf("respawned managed runtime isolation drifted: %v", hostConfig3["Runtime"])
	}
	relay3Inspect, err := inspect(runtime3.Container + "-ctl")
	if err != nil {
		return err
	}
	relay3Host, ok := relay3Inspect["HostConfig"].(map[string]any)
	if !ok || relay3Host["Runtime"] != "runc" {
		return fmt.Errorf("respawned control relay isolation drifted: %v", relay3Host["Runtime"])
	}
	marker, err := os.ReadFile(filepath.Join(contextRoot, "managed", env, "workspace", "managed-sandbox-marker"))
	if err != nil || string(marker) != "sandbox-runtime" {
		return fmt.Errorf("sandbox workspace state did not survive respawn: %q %v", marker, err)
	}
	if err := m.ReleaseBinding(binding); err != nil {
		return err
	}
	if err := m.ReleaseBinding(binding2); err != nil {
		return err
	}
	if err := m.Reap(ctx, time.Now().Add(2*time.Second)); err != nil {
		return err
	}
	stopped, _, err := m.Status(binding)
	if err != nil || stopped.State != supervisor.Stopped {
		return fmt.Errorf("managed runtime did not stop: state=%s err=%v", stopped.State, err)
	}
	for _, name := range []string{runtime3.Container, runtime3.Container + "-ctl", runtime2.Container, runtime2.Container + "-ctl"} {
		if _, err := inspect(name); err == nil {
			return fmt.Errorf("managed container %s survived teardown", name)
		}
	}
	fmt.Printf("managed supervisor canary passed on %s: mock-channel Hermes run, ToolHub admission, private relay pins, mounts, restart/rollover/secondary parity and teardown\n", containerRuntime)
	return nil
}

// managedCanaryContext publishes the synthetic passports and execution selection
// one supervised context needs.
func managedCanaryContext(root, userID, env string) (string, error) {
	contextRoot := filepath.Join(root, userID)
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("# managed canary\n"), 0644); err != nil {
		return "", err
	}
	if err := stack.InitEnvironment(contextRoot, userID, env); err != nil {
		return "", err
	}
	for name, body := range map[string]string{
		"stack":  "identity: {user: " + userID + ", infra: true}\nservices:\n  hermes-runtime: {mode: supervised}\n  model-relay: {mode: shared}\n  egress-relay: {mode: per-space}\n  cliproxy: {mode: shared}\n  toolhub: {mode: shared}\n  credential-broker: {mode: shared}\n  communication-hub: {mode: shared}\n  supervisor: {mode: shared}\n",
		"config": "runtime: {timezone: UTC}\nmemory: {enabled: false}\nchannels:\n  telegram: {bot: {ref: telegram-bot-contract}}\n",
		"models": "default: synthetic\nroutes: {synthetic: {ladder: [upstream]}}\nservices:\n  upstream: {via: openai-compatible, endpoint: http://cliproxy:8317/v1, models: [{id: synthetic}]}\n",
		"tools":  "binding: {profile_id: " + userID + "-default, generation: 1}\ncore:\n  files: {via: toolhub}\n  exposure: {via: toolhub}\n",
	} {
		if err := os.WriteFile(filepath.Join(contextRoot, "passports", name+".yaml"), []byte(body), 0600); err != nil {
			return "", err
		}
	}
	exposureDir := filepath.Join(contextRoot, "connections", "exposure")
	if err := os.MkdirAll(exposureDir, 0750); err != nil {
		return "", err
	}
	const exposureConfig = "schema: 1\nprovider: sish\nrelay:\n  host: fixture.invalid\n  user: canary\n  domain: fixture.invalid\n  host_keys: [\"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX\"]\nzone: canary\nkey_ref: file:fixture-key.pem\nendpoints:\n  canary: {target: hub_public_listener}\n"
	if err := os.WriteFile(filepath.Join(exposureDir, "config.yaml"), []byte(exposureConfig), 0640); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(exposureDir, "fixture-key.pem"), []byte("synthetic canary key"), 0640); err != nil {
		return "", err
	}
	catalogDocs, err := catalog.Generate()
	if err != nil {
		return "", err
	}
	revision, err := passport.CompileAuthored(contextRoot, env, time.Now().UTC(), catalogDocs, stack.ReservedSiblingPorts(contextRoot), map[string][]string{})
	if err != nil {
		return "", err
	}
	if err := revision.Publish(contextRoot); err != nil {
		return "", err
	}
	selection := stack.ExecutionSelection{Schema: 1, User: userID, Environment: env, Mode: "supervisor", SupervisorURL: "http://supervisor:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(contextRoot, stack.ExecutionPath(env)), selectionBody, 0600); err != nil {
		return "", err
	}
	if err := stack.RenderEnvironment(contextRoot, root, env); err != nil {
		return "", err
	}
	files := map[string][]byte{
		"managed-runtime." + env + ".env": []byte("OPENAI_API_KEY=probe-key\nHUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"),
		"runtime.auth":                    []byte("HUB_RUNTIME_AUTH=managed-canary-auth-0123456789abcdef\n"),
		"SOUL.md":                         []byte("# managed canary\n"),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(contextRoot, name), content, 0600); err != nil {
			return "", fmt.Errorf("write %s: %w", name, err)
		}
	}
	// SOUL.md is bind-mounted into the container (uid 10001): Linux keeps the
	// host file owner, so it must be world-readable like the materialized
	// effective config.
	if err := os.Chmod(filepath.Join(contextRoot, "SOUL.md"), 0644); err != nil {
		return "", err
	}
	for _, name := range []string{"runtime", "hermes", "home", "cache", "workspace"} {
		dir := filepath.Join(contextRoot, "managed", env, name)
		if err := os.MkdirAll(dir, 0777); err != nil {
			return "", err
		}
		if err := os.Chmod(dir, 0777); err != nil {
			return "", err
		}
	}
	if err := os.Chmod(contextRoot, 0777); err != nil {
		return "", err
	}
	return contextRoot, nil
}

// managedMockChannelGate drives the production channel pipeline end to end:
// a real communication gateway (durable spool, dispatch workers and the
// delivery loop) admits a /v1/mock/message, and the supervisor's /v1/jobs
// admission cold-spawns the managed runtime under the selected sandbox inside
// that request. The captured mock-sent delivery and the completed job mapping
// are the durable proof the reply crossed the whole path; a repeated
// client_id must never enqueue a second run.
func managedMockChannelGate(ctx context.Context, m *supervisor.Manager, runtimeAuth, root, userID string) error {
	supervisorSrv := httptest.NewServer(m.Handler())
	defer supervisorSrv.Close()
	spoolDir := filepath.Join(root, "gateway-spool")
	gateway, err := communication.New(communication.Config{
		Supervised:     true,
		OrganizationID: "personal",
		Users: []communication.User{{
			ID: userID, Enabled: true, TelegramIDs: []int64{11},
			RuntimeID: userID, PolicyVersion: "canary",
		}},
		TelegramToken: "synthetic-mock-channel-token",
		// Dead loopback port: the real Telegram poll leg must never reach an
		// external API inside the sandbox bench; only the mock channel is
		// exercised.
		APIBaseURL:  "http://127.0.0.1:9",
		SpoolDir:    spoolDir,
		RuntimeURL:  supervisorSrv.URL,
		RuntimeAuth: runtimeAuth,
		ControlAuth: "mock-channel-control-0123456789abcdef",
		Workers:     2,
		PollTimeout: time.Second,
	})
	if err != nil {
		return fmt.Errorf("mock channel gateway: %w", err)
	}
	gatewayCtx, gatewayCancel := context.WithCancel(ctx)
	gatewayDone := make(chan error, 1)
	go func() { gatewayDone <- gateway.Run(gatewayCtx) }()
	defer func() {
		gatewayCancel()
		select {
		case <-gatewayDone:
		case <-time.After(15 * time.Second):
		}
	}()
	gatewaySrv := httptest.NewServer(gateway.Handler())
	defer gatewaySrv.Close()
	const chatID = int64(70001)
	admit := func() (map[string]string, error) {
		body, err := json.Marshal(map[string]any{
			"chat_id":   chatID,
			"text":      "sandbox channel probe",
			"client_id": "mock-sandbox-" + userID,
		})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewaySrv.URL+"/v1/mock/message", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer mock-channel-control-0123456789abcdef")
		req.Header.Set("X-Hub-Principal", userID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var out map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("mock admission decode failed (HTTP %d): %w", resp.StatusCode, err)
		}
		if resp.StatusCode != http.StatusOK || out["job_id"] == "" || out["task_id"] == "" {
			return nil, fmt.Errorf("mock admission rejected: HTTP %d %v", resp.StatusCode, out)
		}
		return out, nil
	}
	admitted, err := admit()
	if err != nil {
		return err
	}
	jobID := admitted["job_id"]
	if !strings.HasPrefix(jobID, "mock-") {
		return fmt.Errorf("unexpected mock job id %q", jobID)
	}
	receiptPath := filepath.Join(spoolDir, "mock-sent", "job-"+jobID+"-response.json")
	mappingPath := filepath.Join(spoolDir, "mappings", jobID+".json")
	deadline := time.Now().Add(8 * time.Minute)
	for {
		data, readErr := os.ReadFile(receiptPath)
		if readErr == nil {
			var receipt struct {
				Principal string `json:"principal"`
				Delivery  struct {
					JobID   string `json:"job_id"`
					TaskID  string `json:"task_id"`
					Channel string `json:"channel"`
					ChatID  int64  `json:"chat_id"`
					Text    string `json:"text"`
				} `json:"delivery"`
			}
			if err := json.Unmarshal(data, &receipt); err != nil {
				return fmt.Errorf("mock receipt unreadable: %w", err)
			}
			d := receipt.Delivery
			if receipt.Principal != userID || d.JobID != jobID || d.TaskID != admitted["task_id"] || d.Channel != "mock" || d.ChatID != chatID || !strings.Contains(d.Text, "canary") {
				return errors.New("mock receipt owner/audience mismatch")
			}
			break
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return fmt.Errorf("mock receipt unreadable: %w", readErr)
		}
		if mapping, mapErr := os.ReadFile(mappingPath); mapErr == nil {
			var record struct {
				Status string `json:"status"`
			}
			if json.Unmarshal(mapping, &record) == nil && (record.Status == "failed" || record.Status == "cancelled" || record.Status == "interrupted") {
				return fmt.Errorf("mock channel job terminated without delivery: %s", record.Status)
			}
		}
		if time.Now().After(deadline) {
			return errors.New("mock channel delivery never reached mock-sent")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	mapping, err := os.ReadFile(mappingPath)
	if err != nil {
		return fmt.Errorf("mock channel job mapping missing: %w", err)
	}
	var record struct {
		Status    string `json:"status"`
		RunID     string `json:"run_id"`
		SessionID string `json:"session_id"`
		Channel   string `json:"channel"`
	}
	if err := json.Unmarshal(mapping, &record); err != nil || record.Status != "completed" || record.RunID == "" || record.SessionID == "" || record.Channel != "mock" {
		return errors.New("mock channel did not complete a real run")
	}
	// Idempotent admission: the same client_id returns the same job without a
	// second enqueue, so no second delivery may appear.
	again, err := admit()
	if err != nil {
		return err
	}
	if again["job_id"] != jobID || again["task_id"] != admitted["task_id"] {
		return errors.New("idempotent mock admission drifted")
	}
	time.Sleep(5 * time.Second)
	entries, err := os.ReadDir(filepath.Join(spoolDir, "mock-sent"))
	if err != nil {
		return fmt.Errorf("mock outbox unreadable: %w", err)
	}
	if len(entries) != 1 {
		return fmt.Errorf("idempotent client_id produced %d mock deliveries", len(entries))
	}
	return nil
}
