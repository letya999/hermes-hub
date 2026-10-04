//go:build integration

package devcheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/supervisor"
	"gopkg.in/yaml.v3"
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
	agentNet := "hermes-hub-agent-" + userID + "-" + env
	sharedNet, controlNet := "hermes-hub-runtime", "hermes-hub-control"
	containers, ownedNetworks := []string{}, []string{}
	sharedPreexisted, controlPreexisted := false, false
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
	networkExists := func(name string) bool {
		return exec.CommandContext(ctx, "docker", "network", "inspect", "--format", "{{.Id}}", name).Run() == nil
	}
	sharedPreexisted = networkExists(sharedNet)
	controlPreexisted = networkExists(controlNet)
	if _, err := docker("network", "create", "--internal", agentNet); err != nil {
		return err
	}
	ownedNetworks = append(ownedNetworks, agentNet)
	if !sharedPreexisted {
		if _, err := docker("network", "create", sharedNet); err != nil {
			return err
		}
		ownedNetworks = append(ownedNetworks, sharedNet)
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
 def do_POST(self):
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
  socket.create_connection(("toolhub",8090),1).close(); socket.create_connection(("model-relay",8318),1).close(); sys.exit(0)
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
	cfg := supervisor.Config{SpacesRoot: root, Image: image, RuntimeAuth: "supervisor-control-0123456789abcdef", Network: sharedNet, WarmTTL: time.Second, PortBase: 28600}
	cfg.Command = func(callCtx context.Context, args ...string) ([]byte, error) {
		output, err := exec.CommandContext(callCtx, "docker", args...).CombinedOutput()
		if len(args) != 0 && args[0] == "rm" {
			name := args[len(args)-1]
			if logs, logErr := exec.CommandContext(callCtx, "docker", "logs", name).CombinedOutput(); logErr == nil && len(logs) != 0 {
				fmt.Printf("--- logs %s ---\n%s\n", name, logs)
			}
		}
		return output, err
	}
	m, err := supervisor.New(cfg)
	if err != nil {
		return err
	}
	binding := supervisor.Binding{PrincipalID: userID, ContextID: userID, RuntimeID: userID, RuntimeMode: "gateway", UserID: userID, OrganizationID: "personal", PolicyVersion: "canary", ContextRoot: contextRoot}
	runtime, err := m.Ensure(ctx, binding)
	if err != nil {
		return fmt.Errorf("managed supervised spawn: %w", err)
	}
	containers = append(containers, runtime.Container, runtime.Container+"-ctl")
	if !runtime.Managed || runtime.State != supervisor.Busy {
		return fmt.Errorf("managed runtime not busy: %+v", runtime)
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
	envOut, envErr := exec.CommandContext(ctx, "docker", "exec", runtime.Container, "env").CombinedOutput()
	if envErr != nil {
		return fmt.Errorf("managed env scan failed: %v: %s", envErr, envOut)
	}
	for _, line := range strings.Split(string(envOut), "\n") {
		key := strings.SplitN(line, "=", 2)[0]
		if strings.Contains(key, "BROKER") || strings.Contains(key, "CREDENTIAL") || strings.Contains(key, "TOKEN") || strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GH_") {
			return fmt.Errorf("managed runtime env leaks control-plane secret variable %s", key)
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
	networks2 := runtime2Inspect["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
	if len(networks2) != 1 || networks2[agentNet2] == nil {
		return fmt.Errorf("secondary runtime joined unexpected networks: %v", networks2)
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
	if !controlPreexisted {
		ownedNetworks = append(ownedNetworks, controlNet)
	}
	fmt.Println("managed supervisor canary passed: real spawn on internal agent network, relay-only control route, live isolation attestation, restart/rollover/secondary parity and combined teardown")
	return nil
}

// managedCanaryContext writes the reviewed settings, execution selection,
// env files and managed state directories one supervised context needs.
func managedCanaryContext(root, userID, env string) (string, error) {
	contextRoot := filepath.Join(root, userID)
	if err := os.MkdirAll(contextRoot, 0700); err != nil {
		return "", err
	}
	settings := stack.Settings{Schema: 1, User: userID, Environment: env, Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, CapabilityMode: "managed", CapabilityProfileID: "alice-default", CapabilityGeneration: 1}
	body, err := yaml.Marshal(settings)
	if err != nil {
		return "", err
	}
	source, err := yaml.Marshal(stack.Config(settings))
	if err != nil {
		return "", err
	}
	selection := stack.ExecutionSelection{Schema: 1, User: userID, Environment: env, Mode: "supervisor", SupervisorURL: "http://localhost:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
	selectionBody, err := json.Marshal(selection)
	if err != nil {
		return "", err
	}
	files := map[string][]byte{
		"settings.yaml":                   body,
		"managed-runtime." + env + ".env": []byte("OPENAI_API_KEY=probe-key\nHUB_TOOLHUB_ENDPOINT=http://toolhub:8090/mcp\n"),
		"runtime.auth":                    []byte("HUB_RUNTIME_AUTH=managed-canary-auth-0123456789abcdef\n"),
		"SOUL.md":                         []byte("# managed canary\n"),
		"hermes." + env + ".yaml":         source,
		stack.ExecutionPath(env):          selectionBody,
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
	for _, name := range []string{"runtime", "hermes", "home", "cache"} {
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
