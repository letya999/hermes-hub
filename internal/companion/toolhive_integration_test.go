//go:build integration

package companion

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// This tests the installed upstream CLI, not a mock and not an untrusted MCP.
// Set HUB_TEST_TOOLHIVE to an absolute trusted ToolHive binary path explicitly.
func TestToolHiveRemoteWorkloadWithoutDocker(t *testing.T) {
	binary := os.Getenv("HUB_TEST_TOOLHIVE")
	if binary == "" {
		t.Skip("HUB_TEST_TOOLHIVE not selected")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute trusted ToolHive binary path required")
	}
	private := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "http://127.0.0.1:1/mcp", "--name", "hermes-no-docker-probe", "--foreground", "--ignore-globally=false")
	command.Env = []string{
		"SystemRoot=" + os.Getenv("SystemRoot"),
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + private, "USERPROFILE=" + private,
		"APPDATA=" + private, "LOCALAPPDATA=" + private,
		"XDG_CONFIG_HOME=" + private, "XDG_DATA_HOME=" + private,
		"XDG_STATE_HOME=" + private, "XDG_CACHE_HOME=" + private,
		"TOOLHIVE_RUNTIME=docker", "TOOLHIVE_SKIP_UPDATE_CHECK=true",
		"TOOLHIVE_USAGE_METRICS_ENABLED=false",
		"DOCKER_HOST=npipe:////./pipe/hermes-no-docker-probe",
		"TOOLHIVE_DOCKER_SOCKET=npipe:////./pipe/hermes-no-docker-probe",
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if ctx.Err() != nil {
		t.Fatalf("CLI probe exceeded timeout: %v", ctx.Err())
	}
	if err == nil || !strings.Contains(output.String(), "runtime") {
		t.Fatalf("unexpected remote bootstrap result: %v, %s", err, output.String())
	}
	t.Logf("installed ToolHive rejected remote workload without runtime: %s", strings.TrimSpace(output.String()))
}

func TestToolHiveShimDocker(t *testing.T) {
	archive := os.Getenv("HUB_TEST_TOOLHIVE_ARCHIVE")
	fixture := os.Getenv("HUB_TEST_COMPANION_LINUX")
	if archive == "" || fixture == "" {
		t.Skip("verified ToolHive archive and cross-compiled synthetic fixture not selected")
	}
	if !filepath.IsAbs(archive) || !filepath.IsAbs(fixture) {
		t.Fatal("absolute private fixture paths required")
	}
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != "7ed4b9cbe7e3e052f3a9a2a3268d6e13b02ad492a740086d989720c7f30f4ea9" {
		t.Fatal("official ToolHive v0.48.0 Linux archive digest mismatch")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var toolhive []byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "thv" && header.Typeflag == tar.TypeReg && header.Size < 160<<20 {
			toolhive, err = io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	fixtureBytes, err := os.ReadFile(fixture)
	if err != nil || len(toolhive) == 0 || !bytes.HasPrefix(fixtureBytes, []byte("\x7fELF")) {
		t.Fatalf("Linux fixture/binary missing: %v", err)
	}
	var upload bytes.Buffer
	writer := tar.NewWriter(&upload)
	if err := writer.WriteHeader(&tar.Header{Name: "state", Typeflag: tar.TypeDir, Mode: 0700, Uid: 10001, Gid: 10001}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{"thv", toolhive}, {"companion.test", fixtureBytes}} {
		if err := writer.WriteHeader(&tar.Header{Name: file.name, Mode: 0755, Uid: 10001, Gid: 10001, Size: int64(len(file.data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(file.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	private := t.TempDir()
	name := "hermes-thv-probe-" + strings.ToLower(rand.Text())
	volume := name + "-files"
	docker := func(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "docker", append([]string{"--config", private, "--host", "npipe:////./pipe/dockerDesktopLinuxEngine"}, args...)...)
		command.Env = []string{"SystemRoot=" + os.Getenv("SystemRoot"), "PATH=" + os.Getenv("PATH"), "USERPROFILE=" + private, "HOME=" + private, "APPDATA=" + private}
		command.Stdin = stdin
		return command.CombinedOutput()
	}
	bridgeName := name + "-mcp"
	var bridgeID string
	var createdVolumes []string
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, _ = docker(cleanup, nil, "rm", "--force", name)
		_, _ = docker(cleanup, nil, "rm", "--force", bridgeName)
		for _, owned := range createdVolumes {
			if output, err := docker(cleanup, nil, "volume", "rm", owned); err != nil {
				t.Errorf("synthetic volume cleanup: %s, %v", output, err)
			}
		}
	}()
	for _, role := range []string{"mcp", "proxy"} {
		containerName, containerVolume, network := bridgeName, volume+"-mcp", "none"
		mode := "-test.run=^TestToolHiveIsolatedBridge$"
		environment := "HUB_TEST_ISOLATED_BRIDGE=1"
		if role == "proxy" {
			containerName, containerVolume, network = name, volume, "container:"+bridgeID
			mode, environment = "-test.run=^TestToolHiveRemoteOnlyShim$", "HUB_TEST_SHIM=1"
		}
		if output, err := docker(ctx, nil, "volume", "create", "--label", "hermes.verification=toolhive-shim", containerVolume); err != nil {
			t.Fatalf("volume create: %s, %v", output, err)
		}
		createdVolumes = append(createdVolumes, containerVolume)
		args := []string{"create", "--pull=never", "--name", containerName, "--label", "hermes.verification=toolhive-shim", "--user", "10001:10001", "--read-only", "--network", network, "--cap-drop", "ALL", "--security-opt", "no-new-privileges=true", "--memory", "512m", "--memory-swap", "512m", "--cpus", "1", "--pids-limit", "128", "--log-driver", "none", "--tmpfs", "/tmp:uid=10001,gid=10001,mode=1777,size=128m", "--mount", "type=volume,source=" + containerVolume + ",target=/verify", "--env", environment}
		if role == "proxy" {
			args = append(args, "--env", "HUB_TEST_REMOTE_BRIDGE=127.0.0.1:39123")
		}
		args = append(args, "--entrypoint", "/verify/companion.test", "node@sha256:cd9f682fa2885cd1056e830424764158570061c59736a1da836bc3d73df095ae", "-test.v", mode)
		if output, err := docker(ctx, nil, args...); err != nil {
			t.Fatalf("bounded container create: %s, %v", output, err)
		}
		if role == "mcp" {
			identity, err := docker(ctx, nil, "inspect", "--format", "{{.Id}}", bridgeName)
			bridgeID = strings.TrimSpace(string(identity))
			if decoded, invalid := hex.DecodeString(bridgeID); err != nil || invalid != nil || len(decoded) != 32 {
				t.Fatal("invalid owned bridge container identity")
			}
		}
		if output, err := docker(ctx, bytes.NewReader(upload.Bytes()), "cp", "-", containerName+":/verify"); err != nil {
			t.Fatalf("private binary copy: %s, %v", output, err)
		}
		profile, err := docker(ctx, nil, "inspect", "--format", "{{json .HostConfig}}", containerName)
		if err != nil {
			t.Fatal(err)
		}
		var configuration struct {
			Memory, MemorySwap, NanoCpus, PidsLimit int64
			Privileged, ReadonlyRootfs              bool
			NetworkMode, PidMode                    string
			CapAdd, CapDrop, SecurityOpt, Binds     []string
			Mounts                                  []struct{ Type, Source, Target string }
		}
		if json.Unmarshal(profile, &configuration) != nil || configuration.Memory != 512<<20 || configuration.MemorySwap != 512<<20 || configuration.NanoCpus != 1_000_000_000 || configuration.PidsLimit != 128 || configuration.Privileged || !configuration.ReadonlyRootfs || configuration.NetworkMode != network || configuration.PidMode != "" || len(configuration.CapAdd) != 0 || len(configuration.Binds) != 0 || len(configuration.CapDrop) != 1 || configuration.CapDrop[0] != "ALL" || len(configuration.SecurityOpt) != 1 || configuration.SecurityOpt[0] != "no-new-privileges=true" || len(configuration.Mounts) != 1 || configuration.Mounts[0].Type != "volume" || configuration.Mounts[0].Source != containerVolume || configuration.Mounts[0].Target != "/verify" {
			t.Fatalf("pre-start %s Docker profile differs from required fixture bounds: %+v; expected network %q", role, configuration, network)
		}
		user, err := docker(ctx, nil, "inspect", "--format", "{{.Config.User}}", containerName)
		if err != nil || strings.TrimSpace(string(user)) != "10001:10001" {
			t.Fatal("non-root user not enforced before start")
		}
		if role == "mcp" {
			if output, err := docker(ctx, nil, "start", bridgeName); err != nil {
				t.Fatalf("isolated stdio bridge start: %s, %v", output, err)
			}
		}
	}
	output, err := docker(ctx, nil, "start", "--attach", name)
	t.Logf("isolated upstream ToolHive probe: %s", output)
	if err != nil || !bytes.Contains(output, []byte("--- PASS: TestToolHiveRemoteOnlyShim")) {
		t.Fatalf("remote-only shim probe failed: %v", err)
	}
}

// Linux-container probe: the socket below is a deny-all bootstrap shim, never
// a connection or reverse proxy to Docker. Only our synthetic MCP executes.
func TestToolHiveRemoteOnlyShim(t *testing.T) {
	if os.Getenv("HUB_TEST_SHIM") != "1" {
		t.Skip("isolated Linux probe not selected")
	}
	if err := os.WriteFile("/verify/state/control-only", []byte("synthetic control state"), 0600); err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	socket := filepath.Join(private, "bootstrap.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []string
	shim := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if (r.Method == "GET" || r.Method == "HEAD") && r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.53")
			w.Header().Set("Docker-Experimental", "false")
			w.Header().Set("OSType", "linux")
			_, _ = w.Write([]byte("OK"))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1.53/containers/json" {
			_, _ = w.Write([]byte("[]"))
			return
		}
		if r.Method == "GET" && r.URL.Path == "/v1.53/containers/synthetic-remote/json" {
			http.Error(w, `{"message":"no container; remote-only bootstrap"}`, http.StatusNotFound)
			return
		}
		// Deliberately no container/image/network/volume API emulation.
		http.Error(w, `{"message":"remote-only bootstrap; Docker operations forbidden"}`, http.StatusForbidden)
	})}
	go func() { _ = shim.Serve(listener) }()
	defer shim.Close()
	shimClient := &http.Client{Timeout: time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	defer shimClient.CloseIdleConnections()
	for _, path := range []string{"/v1.53/containers/create", "/v1.53/images/create", "/v1.53/networks/create", "/v1.53/containers/synthetic-remote/start"} {
		response, err := shimClient.Post("http://bootstrap"+path, "application/json", strings.NewReader(`{"HostConfig":{"Privileged":true}}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("bootstrap shim allowed Docker mutation %s", path)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const token = "synthetic-bridge-token-0123456789012345678901"
	bridgeAddress := os.Getenv("HUB_TEST_REMOTE_BRIDGE")
	if bridgeAddress == "" {
		bridgeListener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		bridgeAddress = bridgeListener.Addr().String()
		_ = bridgeListener.Close()
		t.Setenv("HUB_TEST_CHILD", "1")
		t.Setenv("HUB_TEST_BRIDGE_TOKEN", token)
		configuration, err := yaml.Marshal(Config{Listen: bridgeAddress, TokenEnv: "HUB_TEST_BRIDGE_TOKEN", Command: []string{os.Args[0], "-test.run=^TestCompanionChild$"}, AllowedTools: []string{"echo"}})
		if err != nil {
			t.Fatal(err)
		}
		configurationPath := filepath.Join(private, "bridge.yaml")
		if err := os.WriteFile(configurationPath, configuration, 0600); err != nil {
			t.Fatal(err)
		}
		bridgeDone := make(chan error, 1)
		go func() { bridgeDone <- Run(ctx, configurationPath) }()
		defer func() {
			cancel()
			select {
			case err := <-bridgeDone:
				if err != nil {
					t.Errorf("stdio bridge shutdown: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("stdio bridge did not stop")
			}
		}()
	}
	portListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strings.Split(portListener.Addr().String(), ":")[1]
	_ = portListener.Close()
	command := exec.CommandContext(ctx, "/verify/thv", "run", "http://"+bridgeAddress+"/mcp", "--name", "synthetic-remote", "--foreground", "--ignore-globally=false", "--proxy-port", port, "--stateless", "--remote-forward-headers-secret", "Authorization=BRIDGE_AUTH")
	command.Env = []string{"HOME=" + private, "XDG_CONFIG_HOME=" + private, "XDG_DATA_HOME=" + private, "XDG_STATE_HOME=" + private, "XDG_CACHE_HOME=" + private, "TOOLHIVE_DOCKER_SOCKET=" + socket, "TOOLHIVE_RUNTIME=docker", "TOOLHIVE_SKIP_UPDATE_CHECK=true", "TOOLHIVE_USAGE_METRICS_ENABLED=false"}
	command.Env = append(command.Env, "TOOLHIVE_SECRETS_PROVIDER=environment", "TOOLHIVE_SECRET_BRIDGE_AUTH=Bearer "+token)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	// A sibling process models an MCP with the same UID, not its trusted parent.
	// Never print the synthetic secret: this diagnostic decides whether sharing
	// a process namespace is suitable for the eventual production controller.
	sibling := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestToolHiveSiblingSecretProbe$")
	sibling.Env = []string{"HUB_TEST_CONTROL_PID=" + strconv.Itoa(command.Process.Pid)}
	siblingOutput, siblingErr := sibling.CombinedOutput()
	if siblingErr != nil {
		t.Fatalf("same-UID isolation diagnostic failed: %v", siblingErr)
	}
	if bytes.Contains(siblingOutput, []byte("control-secret-visible")) {
		t.Log("same-UID sibling sharing ToolHive PID namespace can read its authorization; this diagnostic is not the isolated MCP container")
	} else if bytes.Contains(siblingOutput, []byte("control-secret-inaccessible")) {
		t.Log("same-UID sibling could not read ToolHive environment in this fixture")
	} else {
		t.Fatal("same-UID isolation diagnostic returned no evidence")
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("ToolHive child did not stop")
		}
		mu.Lock()
		t.Logf("bootstrap requests: %v", requests)
		mu.Unlock()
		t.Logf("ToolHive output: %s", output.String())
	}()
	endpoint := "http://127.0.0.1:" + port + "/mcp"
	for {
		select {
		case err := <-done:
			done <- err
			t.Fatalf("remote bootstrap failed: %v", err)
		default:
		}
		response, err := http.Get(endpoint)
		if err == nil {
			_ = response.Body.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("remote proxy startup timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-shim-probe", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: &http.Client{Timeout: 5 * time.Second}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil || tools == nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
		t.Fatalf("tools/list: %+v, %v", tools, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "synthetic-no-docker"}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("tools/call: %+v, %v", result, err)
	}
	structured, err := json.Marshal(result.StructuredContent)
	if err != nil || !bytes.Equal(structured, []byte(`{"text":"synthetic-no-docker"}`)) {
		t.Fatal("tools/call did not return the exact synthetic input")
	}
	export := exec.CommandContext(ctx, "/verify/thv", "export", "synthetic-remote", filepath.Join(private, "runconfig.json"))
	export.Env = command.Env
	if exported, err := export.CombinedOutput(); err != nil {
		t.Fatalf("Workload Manager export: %s, %v", exported, err)
	}
	runconfig, err := os.ReadFile(filepath.Join(private, "runconfig.json"))
	if err != nil || !bytes.Contains(runconfig, []byte("synthetic-remote")) || bytes.Contains(runconfig, []byte(token)) {
		t.Fatalf("RunConfig missing or contains credential value: %v", err)
	}
	for _, operation := range []string{"stop", "rm"} {
		lifecycle := exec.CommandContext(ctx, "/verify/thv", operation, "synthetic-remote")
		lifecycle.Env = command.Env
		if lifecycleOutput, err := lifecycle.CombinedOutput(); err != nil {
			t.Fatalf("Workload Manager %s: %s, %v", operation, lifecycleOutput, err)
		}
	}
}

func TestToolHiveSiblingSecretProbe(t *testing.T) {
	pid := os.Getenv("HUB_TEST_CONTROL_PID")
	if pid == "" {
		t.Skip("isolated same-UID sibling probe not selected")
	}
	if parsed, err := strconv.Atoi(pid); err != nil || parsed <= 0 {
		t.Fatal("invalid synthetic control PID")
	}
	data, err := os.ReadFile("/proc/" + pid + "/environ")
	if err != nil {
		t.Log("control-secret-inaccessible")
		return
	}
	if !bytes.Contains(data, []byte("TOOLHIVE_SECRET_BRIDGE_AUTH=")) {
		t.Fatal("control environment read but synthetic secret marker absent")
	}
	t.Log("control-secret-visible")
}

func TestToolHiveIsolatedBridge(t *testing.T) {
	if os.Getenv("HUB_TEST_ISOLATED_BRIDGE") != "1" {
		t.Skip("isolated stdio container not selected")
	}
	t.Setenv("HUB_TEST_CHILD", "1")
	t.Setenv("HUB_TEST_BRIDGE_TOKEN", "synthetic-bridge-token-0123456789012345678901")
	configuration, err := yaml.Marshal(Config{Listen: "127.0.0.1:39123", TokenEnv: "HUB_TEST_BRIDGE_TOKEN", Command: []string{os.Args[0], "-test.run=^TestCompanionChild$"}, AllowedTools: []string{"echo"}})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "bridge.yaml")
	if err := os.WriteFile(file, configuration, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := Run(ctx, file); err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
}
