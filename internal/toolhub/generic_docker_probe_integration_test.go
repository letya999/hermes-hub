//go:build integration

package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRealDockerFallbackNamesAndRuntimeProfile(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "linux" {
		t.Skipf("local Linux Docker required: %s %v", out, err)
	}
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("p%02d", i)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			resources := genericFallbackResources(id)
			_ = exec.Command("docker", "rm", "--force", id, resources.ProxyName, resources.RelayName).Run()
			_ = exec.Command("docker", "network", "rm", resources.Network).Run()
			_ = exec.Command("docker", "volume", "rm", resources.ProxyVol, resources.BridgeVol, resources.RelayVol, genericStateVolume(id, 0)).Run()
		}
	})
	seen := map[string]bool{}
	for i, id := range ids {
		resources := genericFallbackResources(id)
		state := genericStateVolume(id, 0)
		for _, name := range []string{resources.Network, resources.ProxyVol, resources.BridgeVol, resources.RelayVol, state} {
			if seen[name] {
				t.Fatalf("duplicate resource name %s", name)
			}
			seen[name] = true
		}
		if out, err := exec.Command("docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp-probe", state).CombinedOutput(); err != nil {
			t.Fatalf("volume %s: %s %v", state, out, err)
		}
		if i >= 2 {
			continue
		}
		if out, err := exec.Command("docker", "network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp-probe", resources.Network).CombinedOutput(); err != nil {
			t.Fatalf("network %s: %s %v", resources.Network, out, err)
		}
		if out, err := exec.Command("docker", "run", "--rm", "--user", "0:0", "--network", "none", "--mount", "type=volume,source="+state+",target=/state", "python:3.12-slim", "chmod", "0777", "/state").CombinedOutput(); err != nil {
			t.Fatalf("prepare state volume %s: %s %v", state, out, err)
		}
		args := []string{"create", "--name", id, "--network", resources.Network, "--read-only", "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--cpus", "0.25", "--memory", "64m", "--memory-swap", "64m", "--pids-limit", "32", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=volume,source=" + state + ",target=/state", "python:3.12-slim", "sleep", "30"}
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create %s: %s %v", id, out, err)
		}
		if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
			t.Fatalf("start %s: %s %v", id, out, err)
		}
	}
	body, err := exec.Command("docker", "inspect", ids[0], ids[1]).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect: %s %v", body, err)
	}
	var containers []genericContainer
	if json.Unmarshal(body, &containers) != nil || len(containers) != 2 {
		t.Fatalf("inspect decode: %s", body)
	}
	volumes := map[string]string{}
	for i, container := range containers {
		if container.HostConfig.Privileged || !container.HostConfig.ReadonlyRootfs || container.HostConfig.NetworkMode == "host" || container.HostConfig.PidMode == "host" || len(container.HostConfig.CapAdd) != 0 || len(container.HostConfig.CapDrop) == 0 || container.HostConfig.CapDrop[0] != "ALL" || !contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || container.Config.User == "" || container.Config.User == "0" || container.Config.User == "0:0" || len(container.HostConfig.Binds) != 0 || envContainsForwardingSecret(container.Config.Env) {
			t.Fatalf("unsafe live profile %d: %+v", i, container.HostConfig)
		}
		if len(container.Mounts) != 1 || container.Mounts[0].Type != "volume" {
			t.Fatalf("expected one named volume, got %+v", container.Mounts)
		}
		volumes[ids[i]] = container.Mounts[0].Name
		if _, err := exec.Command("docker", "exec", ids[i], "python", "-c", "open('/state/marker-'+'"+ids[i]+"','w').write('private')").CombinedOutput(); err != nil {
			t.Fatalf("write state %s: %v", ids[i], err)
		}
	}
	if volumes[ids[0]] == volumes[ids[1]] {
		t.Fatal("two workloads shared a state volume")
	}
	out, err := exec.Command("docker", "exec", ids[0], "python", "-c", "import os; print(os.listdir('/state'))").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), ids[1]) {
		t.Fatalf("cross-user state visible: %s", out)
	}
	hostName := ids[0] + "-host"
	if out, err := exec.Command("docker", "create", "--name", hostName, "--privileged", "--network", "host", "--pid", "host", "-v", "/var/run/docker.sock:/var/run/docker.sock", "python:3.12-slim", "true").CombinedOutput(); err != nil {
		t.Fatalf("unsafe create should still be inspectable: %s %v", out, err)
	}
	inspect, err := exec.Command("docker", "inspect", hostName).CombinedOutput()
	_ = exec.Command("docker", "rm", "--force", hostName).Run()
	if err != nil {
		t.Fatalf("unsafe inspect: %s %v", inspect, err)
	}
	var host []genericContainer
	if json.Unmarshal(inspect, &host) != nil || len(host) != 1 {
		t.Fatalf("unsafe inspect decode: %s", inspect)
	}
	profile := RuntimeSecurityProfile{User: host[0].Config.User, NetworkMode: host[0].HostConfig.NetworkMode, PIDMode: host[0].HostConfig.PidMode, ReadonlyRootfs: host[0].HostConfig.ReadonlyRootfs, Privileged: host[0].HostConfig.Privileged, NoNewPrivileges: contains(host[0].HostConfig.SecurityOpt, "no-new-privileges=true"), CapDrop: host[0].HostConfig.CapDrop, CapAdd: host[0].HostConfig.CapAdd, HostMounts: host[0].HostConfig.Binds, CPUQuota: 1, MemoryBytes: 64 << 20, PIDsLimit: 1, TimeoutSeconds: 1, OutputBytes: 1, SeccompProfile: "mcp.json"}
	if err := profile.Validate(statefulContainerDefinition()); err == nil {
		t.Fatal("privileged host-network docker.sock profile accepted by MCP runtime contract")
	}
	if out, err := exec.Command("docker", "stop", ids[0]).CombinedOutput(); err != nil {
		t.Fatalf("stop %s: %s %v", ids[0], out, err)
	}
	if out, err := exec.Command("docker", "start", ids[0]).CombinedOutput(); err != nil {
		t.Fatalf("restart %s: %s %v", ids[0], out, err)
	}
	restarted, err := exec.Command("docker", "exec", ids[0], "python", "-c", "import os; print(os.listdir('/state'))").CombinedOutput()
	if err != nil || !strings.Contains(string(restarted), ids[0]) {
		t.Fatalf("restart lost state volume: %s %v", restarted, err)
	}
	if out, err := exec.Command("docker", "rm", "--force", ids[0]).CombinedOutput(); err != nil {
		t.Fatalf("remove %s: %s %v", ids[0], out, err)
	}
	survivor, err := exec.Command("docker", "exec", ids[1], "python", "-c", "import os; print(os.listdir('/state'))").CombinedOutput()
	if err != nil || !strings.Contains(string(survivor), ids[1]) {
		t.Fatalf("removing one workload deleted the other state: %s %v", survivor, err)
	}
}

func TestRealDockerSequentialHundredBindings(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "linux" {
		t.Skipf("local Linux Docker required: %s %v", out, err)
	}
	root := t.TempDir()
	sharedDir := filepath.Join(root, "shared")
	if err := os.Mkdir(sharedDir, 0700); err != nil {
		t.Fatal(err)
	}
	sharedEnv := filepath.Join(sharedDir, "credentials.env")
	if err := os.WriteFile(sharedEnv, []byte("TOKEN=shared-ref\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("s%02d", i)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			resources := genericFallbackResources(id)
			_ = exec.Command("docker", "rm", "--force", id).Run()
			_ = exec.Command("docker", "network", "rm", resources.Network).Run()
			_ = exec.Command("docker", "volume", "rm", genericStateVolume(id, 0)).Run()
		}
	})
	seen := map[string]bool{}
	envUses := map[string]int{}
	var createArgs []string
	for i, id := range ids {
		resources := genericFallbackResources(id)
		state := genericStateVolume(id, 0)
		for _, name := range []string{id, resources.Network, state} {
			if seen[name] {
				t.Fatalf("duplicate resource name %s", name)
			}
			seen[name] = true
		}
		envFile := sharedEnv
		if i < 95 {
			workspace := filepath.Join(root, id)
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			envFile = filepath.Join(workspace, "credentials.env")
			if err := os.WriteFile(envFile, []byte(fmt.Sprintf("TOKEN=user-%02d\n", i)), 0600); err != nil {
				t.Fatal(err)
			}
		}
		envUses[envFile]++
		if out, err := exec.Command("docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp-seq", state).CombinedOutput(); err != nil {
			t.Fatalf("volume %s: %s %v", state, out, err)
		}
		if out, err := exec.Command("docker", "network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp-seq", resources.Network).CombinedOutput(); err != nil {
			t.Fatalf("network %s: %s %v", resources.Network, out, err)
		}
		if out, err := exec.Command("docker", "run", "--rm", "--user", "0:0", "--network", "none", "--mount", "type=volume,source="+state+",target=/state", "python:3.12-slim", "chmod", "0777", "/state").CombinedOutput(); err != nil {
			t.Fatalf("prepare state volume %s: %s %v", state, out, err)
		}
		args := []string{"create", "--name", id, "--network", resources.Network, "--read-only", "--user", "65534:65534", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--cpus", "0.25", "--memory", "64m", "--memory-swap", "64m", "--pids-limit", "32", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=volume,source=" + state + ",target=/state", "--env-file", envFile, "python:3.12-slim", "sleep", "15"}
		createArgs = append(createArgs, strings.Join(args, " "))
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create %s: %s %v", id, out, err)
		}
		if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
			t.Fatalf("start %s: %s %v", id, out, err)
		}
		body, err := exec.Command("docker", "inspect", id).CombinedOutput()
		if err != nil {
			t.Fatalf("inspect %s: %s %v", id, body, err)
		}
		var containers []genericContainer
		if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
			t.Fatalf("inspect decode %s: %s", id, body)
		}
		container := containers[0]
		if container.HostConfig.Privileged || !container.HostConfig.ReadonlyRootfs || container.HostConfig.NetworkMode == "host" || container.HostConfig.PidMode == "host" || len(container.HostConfig.CapDrop) == 0 || container.HostConfig.CapDrop[0] != "ALL" || !contains(container.HostConfig.SecurityOpt, "no-new-privileges=true") || container.Config.User == "0:0" || len(container.HostConfig.Binds) != 0 {
			t.Fatalf("unsafe live profile %s: %+v", id, container.HostConfig)
		}
		if !envContains(container.Config.Env, "TOKEN=") {
			t.Fatalf("env-file not applied for %s: %v", id, container.Config.Env)
		}
		if i == 0 {
			if _, err := exec.Command("docker", "exec", id, "python", "-c", "open('/state/marker-s00','w').write('private')").CombinedOutput(); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("docker", "stop", id).CombinedOutput(); err != nil {
				t.Fatalf("stop %s: %s %v", id, out, err)
			}
			if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
				t.Fatalf("restart %s: %s %v", id, out, err)
			}
			restarted, err := exec.Command("docker", "exec", id, "python", "-c", "import os; print(os.listdir('/state'))").CombinedOutput()
			if err != nil || !strings.Contains(string(restarted), "marker-s00") {
				t.Fatalf("restart lost state: %s %v", restarted, err)
			}
		}
		if out, err := exec.Command("docker", "rm", "--force", id).CombinedOutput(); err != nil {
			t.Fatalf("remove %s: %s %v", id, out, err)
		}
		if out, err := exec.Command("docker", "network", "rm", resources.Network).CombinedOutput(); err != nil {
			t.Fatalf("network rm %s: %s %v", resources.Network, out, err)
		}
	}
	joined := strings.Join(createArgs, "\n")
	if strings.Contains(joined, "TOKEN=shared-ref") || strings.Contains(joined, "TOKEN=user-") {
		t.Fatal("credential values entered Docker arguments")
	}
	if envUses[sharedEnv] != 5 {
		t.Fatalf("shared credential reference uses: %v", envUses)
	}
	if len(seen) != 300 {
		t.Fatalf("expected 100 unique name triples, got %d", len(seen))
	}
}

func envContains(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func TestRealDockerCloseoutImageReuseSecretAndRevoke(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.OSType}}").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "linux" {
		t.Skipf("local Linux Docker required: %s %v", out, err)
	}
	const image = "python:3.12-slim"
	const token = "closeout-probe-token-not-a-real-secret-01"
	ids := []string{"cl0", "cl1"}
	relay := "cl0-relay"
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "--force", ids[0], ids[1], relay).Run()
		for _, id := range ids {
			resources := genericFallbackResources(id)
			_ = exec.Command("docker", "network", "rm", resources.Network).Run()
			_ = exec.Command("docker", "volume", "rm", genericStateVolume(id, 0)).Run()
		}
	})
	root := t.TempDir()
	imageID, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		if out, pullErr := exec.Command("docker", "pull", image).CombinedOutput(); pullErr != nil {
			t.Fatalf("pull %s: %s %v", image, out, pullErr)
		}
		imageID, err = exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
		if err != nil {
			t.Fatal(err)
		}
	}
	sharedImage := strings.TrimSpace(string(imageID))
	server := `from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def do_POST(self):
        n=int(self.headers.get("Content-Length","0"))
        self.rfile.read(n)
        open("/state/calls.txt","a").write("hit\n")
        self.send_response(200)
        self.send_header("Content-Type","text/plain")
        self.end_headers()
        self.wfile.write(b"ok")
    def log_message(self,*a):
        pass
HTTPServer(("0.0.0.0",8765),H).serve_forever()`
	var containerImages []string
	for i, id := range ids {
		resources := genericFallbackResources(id)
		state := genericStateVolume(id, 0)
		envFile := filepath.Join(root, id+".env")
		if err := os.WriteFile(envFile, []byte(fmt.Sprintf("TOKEN=user-%d\n", i)), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("docker", "volume", "create", "--label", "hermes-hub.role=generic-mcp-closeout", state).CombinedOutput(); err != nil {
			t.Fatalf("volume %s: %s %v", state, out, err)
		}
		if out, err := exec.Command("docker", "network", "create", "--internal", "--label", "hermes-hub.role=generic-mcp-closeout", resources.Network).CombinedOutput(); err != nil {
			t.Fatalf("network %s: %s %v", resources.Network, out, err)
		}
		if out, err := exec.Command("docker", "run", "--rm", "--user", "0:0", "--network", "none", "--mount", "type=volume,source="+state+",target=/state", image, "chmod", "0777", "/state").CombinedOutput(); err != nil {
			t.Fatalf("prepare %s: %s %v", state, out, err)
		}
		args := []string{"create", "--name", id, "--network", resources.Network, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--cpus", "0.25", "--memory", "64m", "--memory-swap", "64m", "--pids-limit", "32", "--tmpfs", "/tmp:rw,nosuid,nodev,size=16m", "--mount", "type=volume,source=" + state + ",target=/state", "--env-file", envFile}
		if i == 0 {
			args = append(args, "--publish", "127.0.0.1::8765/tcp")
		}
		args = append(args, image, "python", "-c", server)
		if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
			t.Fatalf("create %s: %s %v", id, out, err)
		}
		if i == 0 {
			if out, err := exec.Command("docker", "network", "connect", "bridge", id).CombinedOutput(); err != nil {
				t.Fatalf("publish %s: %s %v", id, out, err)
			}
		}
		if out, err := exec.Command("docker", "start", id).CombinedOutput(); err != nil {
			t.Fatalf("start %s: %s %v", id, out, err)
		}
		inspect, err := exec.Command("docker", "inspect", "--format", "{{.Image}} {{.Id}} {{range .Mounts}}{{.Name}}{{end}}", id).Output()
		if err != nil {
			t.Fatal(err)
		}
		containerImages = append(containerImages, strings.TrimSpace(string(inspect)))
	}
	left := strings.Fields(containerImages[0])
	right := strings.Fields(containerImages[1])
	if len(left) < 3 || len(right) < 3 {
		t.Fatalf("inspect fields: %q %q", containerImages[0], containerImages[1])
	}
	if left[0] != sharedImage || right[0] != sharedImage {
		t.Fatalf("image not reused: %q %q shared %q", left[0], right[0], sharedImage)
	}
	if left[1] == right[1] {
		t.Fatal("two bindings shared a container id")
	}
	if left[2] == right[2] {
		t.Fatal("two bindings shared a state volume")
	}
	relayNet := genericFallbackResources(ids[0]).Network
	relayArgs := []string{"create", "--name", relay, "--network", relayNet, "--read-only", "--user", "10001:10001", "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--memory", "32m", "--pids-limit", "16", "--env", "HERMES_BRIDGE_TOKEN=" + token, image, "python", "-c", "import time; time.sleep(30)"}
	if out, err := exec.Command("docker", relayArgs...).CombinedOutput(); err != nil {
		t.Fatalf("relay create: %s %v", out, err)
	}
	if out, err := exec.Command("docker", "start", relay).CombinedOutput(); err != nil {
		t.Fatalf("relay start: %s %v", out, err)
	}
	mcpEnv, err := exec.Command("docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", ids[0]).Output()
	if err != nil {
		t.Fatal(err)
	}
	if envContainsForwardingSecret(strings.Split(strings.TrimSpace(string(mcpEnv)), "\n")) || strings.Contains(string(mcpEnv), token) {
		t.Fatal("MCP container received the forwarding token")
	}
	relayEnv, err := exec.Command("docker", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", relay).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !envHasBridgeToken(strings.Split(strings.TrimSpace(string(relayEnv)), "\n")) {
		t.Fatal("relay did not hold the forwarding token")
	}
	probe := `import os, pathlib
keys=" ".join(os.environ)
if "HERMES_BRIDGE_TOKEN" in keys or "TOOLHIVE_SECRET" in keys or "BRIDGE_AUTH" in keys:
    raise SystemExit("mcp-env-has-token")
for p in pathlib.Path("/proc").iterdir():
    if not p.name.isdigit():
        continue
    try:
        data=(p/"environ").read_bytes()
    except (PermissionError, FileNotFoundError, ProcessLookupError, OSError):
        continue
    if b"HERMES_BRIDGE_TOKEN" in data or b"TOOLHIVE_SECRET" in data or b"BRIDGE_AUTH" in data:
        raise SystemExit("mcp-proc-has-token")
print("mcp-secret-inaccessible")`
	probed, err := exec.Command("docker", "exec", ids[0], "python", "-c", probe).CombinedOutput()
	if err != nil || !strings.Contains(string(probed), "mcp-secret-inaccessible") {
		t.Fatalf("MCP /proc probe: %s %v", probed, err)
	}
	logs, err := exec.Command("docker", "logs", ids[0]).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), token) {
		t.Fatal("forwarding token appeared in MCP logs")
	}
	portOut, err := exec.Command("docker", "port", ids[0], "8765/tcp").Output()
	if err != nil {
		t.Fatalf("port: %s %v", portOut, err)
	}
	hostPort := strings.TrimSpace(string(portOut))
	if i := strings.LastIndex(hostPort, ":"); i >= 0 {
		hostPort = hostPort[i+1:]
	}
	backendURL := "http://127.0.0.1:" + hostPort + "/call"
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, postErr := http.Post(backendURL, "text/plain", strings.NewReader("warmup"))
		if postErr == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP HTTP never became ready: %v", postErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	store, auth, binding := seededStore(t)
	name := ProjectedToolName("google-work", "1.0.0", "search")
	var hits int
	gw := Gateway{Store: store, Backend: backendFunc(func(context.Context, EffectiveBinding, ToolSpec, map[string]any) (BackendResult, error) {
		resp, err := http.Post(backendURL, "text/plain", strings.NewReader("tool"))
		if err != nil {
			return BackendResult{}, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		hits++
		return BackendResult{Text: string(body)}, nil
	})}
	if _, err := gw.call(context.Background(), auth, name, map[string]any{"query": "hello"}); err != nil {
		t.Fatalf("enabled call: %v", err)
	}
	if hits != 1 {
		t.Fatalf("docker backend hits before revoke: %d", hits)
	}
	if err := store.SetBindingStatus(binding.ToolBindingID, RevokedStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.call(context.Background(), auth, name, map[string]any{"query": "stale"}); err == nil {
		t.Fatal("revoked binding still called")
	}
	if hits != 1 {
		t.Fatalf("docker backend was invoked after revoke: %d", hits)
	}
}

func TestWindowsPEBridgeRejectedAgainstBuiltELF(t *testing.T) {
	exe := os.Args[0]
	if strings.EqualFold(filepath.Ext(exe), ".exe") {
		if err := ValidateLinuxBridgeBinary(exe); err == nil {
			t.Fatal("current Windows executable accepted as Linux bridge")
		}
	}
	output := os.Getenv("HERMES_LINUX_BRIDGE")
	if output == "" {
		t.Skip("HERMES_LINUX_BRIDGE not set")
	}
	if err := ValidateLinuxBridgeBinary(output); err != nil {
		t.Fatal(err)
	}
}
