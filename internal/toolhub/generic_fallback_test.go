package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenericDockerFallbackStartsBoundedRemoteProxy(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
	d.Workload.Stateful = true
	d.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("TOKEN=synthetic\n"), 0600); err != nil {
		t.Fatal(err)
	}
	budget, err := NewWorkloadBudget(1, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.WorkspacePath = workspace
	containerValues := genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config")
	containerValues[0]["Mounts"] = []map[string]any{{"Type": "volume", "Source": plan.WorkloadID + "-state-0", "Destination": "/state", "RW": true}, {"Type": "volume", "Source": plan.WorkloadID + "-bridge", "Destination": "/hermes-bridge", "RW": false}}
	network := "hermes-" + plan.WorkloadID
	containerValues = append(containerValues, map[string]any{
		"Image": "relay-image-id", "State": map[string]any{"Running": true},
		"Config":          map[string]any{"User": "10001:10001", "Image": plan.SidecarImages[0], "Env": []string{"HERMES_BRIDGE_TOKEN=" + strings.Repeat("b", 32)}},
		"HostConfig":      containerValues[0]["HostConfig"],
		"Mounts":          []map[string]any{{"Type": "volume", "Source": plan.WorkloadID + "-relay-bin", "Destination": "/hermes-relay", "RW": false}},
		"NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]string{"IPAddress": "172.20.0.4"}, "bridge": map[string]string{"IPAddress": "172.17.0.4"}}},
	})
	containers, _ := json.Marshal(containerValues)
	var calls []string
	var remoteServer *http.Server
	var remoteListener net.Listener
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		calls = append(calls, binary+" "+strings.Join(args, " "))
		if binary == parts[0] && len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
		}
		if binary == parts[0] && len(args) > 0 && args[0] == "run" {
			return []byte("--read-only"), nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "context" {
			return []byte("npipe://./pipe/dockerDesktopLinuxEngine"), nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "info" {
			return []byte("linux"), nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "inspect" && contains(args, "--format") {
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "inspect" && len(args) >= 3 {
			return containers, nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "port" {
			return []byte("127.0.0.1:40001\n"), nil
		}
		return nil, nil
	}
	c.commandEnv = func(_ context.Context, environment map[string]string, binary string, args ...string) ([]byte, error) {
		if binary != parts[0] || len(args) < 1 || (args[0] != "run" && args[0] != "rm" && args[0] != "list") {
			return nil, errors.New("invalid fallback remote invocation")
		}
		if args[0] == "rm" {
			return nil, nil
		}
		if args[0] == "list" {
			if remoteListener == nil {
				return nil, errors.New("remote listener missing")
			}
			return []byte(fmt.Sprintf(`[{"name":"%s","url":"http://127.0.0.1:%d/mcp"}]`, plan.WorkloadID+"-thv", remoteListener.Addr().(*net.TCPAddr).Port)), nil
		}
		if environment["TOOLHIVE_SECRET_BRIDGE_AUTH"] == "" {
			return nil, errors.New("bridge secret missing")
		}
		for i := range args {
			if args[i] != "--proxy-port" || i+1 >= len(args) {
				continue
			}
			port, err := strconv.Atoi(args[i+1])
			if err != nil {
				return nil, err
			}
			remoteListener, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return nil, err
			}
			remoteServer = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })}
			go func() { _ = remoteServer.Serve(remoteListener) }()
			remoteID := "generic-workload-thv"
			for i := range args {
				if args[i] == "--name" && i+1 < len(args) {
					remoteID = args[i+1]
				}
			}
			logDir := filepath.Join(environment["HOME"], "toolhive", "logs")
			if err := os.MkdirAll(logDir, 0700); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(logDir, remoteID+".log"), []byte(fmt.Sprintf(`{"endpoint":"http://localhost:%d/mcp"}`, remoteListener.Addr().(*net.TCPAddr).Port)), 0600); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, errors.New("remote proxy port missing")
	}
	workload, err := c.start(context.Background(), plan)
	if err != nil {
		t.Fatalf("fallback: %v; calls=%v", err, calls)
	}
	if !workload.dockerFallback || workload.remoteName == "" || !strings.HasPrefix(workload.endpoint, "http://127.0.0.1:") {
		t.Fatalf("fallback receipt metadata missing: %+v", workload)
	}
	joined := strings.Join(calls, " ")
	if !strings.Contains(joined, "--env-file "+filepath.Join(workspace, "credentials.env")) {
		t.Fatalf("credential env-file was not handed to Docker: %s", joined)
	}
	if strings.Contains(joined, "synthetic") {
		t.Fatalf("credential value leaked into control command: %s", joined)
	}
	if !strings.Contains(joined, "relay --listen") || !strings.Contains(joined, "--token-env HERMES_BRIDGE_TOKEN") {
		t.Fatalf("relay did not receive the forwarding token env: %s", joined)
	}
	mcpCreate := ""
	for _, call := range calls {
		if strings.Contains(call, "docker create --name "+plan.WorkloadID+" ") && !strings.Contains(call, "-relay") && !strings.Contains(call, "-egress") {
			mcpCreate = call
		}
	}
	if mcpCreate == "" || strings.Contains(mcpCreate, "HERMES_BRIDGE_TOKEN=") || strings.Contains(mcpCreate, "TOOLHIVE_SECRET") {
		t.Fatalf("MCP create leaked a forwarding secret: %s", mcpCreate)
	}
	if !strings.Contains(joined, "type=volume,source="+plan.WorkloadID+"-state-0,target=/state") {
		t.Fatalf("state volume was not handed to Docker: %s", joined)
	}
	c.workloads[plan.WorkloadID] = workload
	if err := budget.Acquire(context.Background(), plan.WorkloadID); err != nil {
		t.Fatal(err)
	}
	if got := c.CleanupIdle(context.Background(), time.Now().Add(2*time.Minute)); len(got) != 1 || got[0] != plan.WorkloadID {
		t.Fatalf("fallback idle cleanup: %v", got)
	}
	if remoteServer != nil {
		_ = remoteServer.Close()
	}
	if remoteListener != nil {
		_ = remoteListener.Close()
	}
}

func TestResolveArtifactImagePrefersPinnedThenLoadedName(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	pinned := plan.Image + "@" + plan.Digest
	loadedID := "sha256:" + strings.Repeat("ab", 32)
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "docker" || len(args) < 3 || args[0] != "image" || args[1] != "inspect" {
			t.Fatalf("unexpected command %s %v", binary, args)
		}
		if args[2] == pinned {
			return nil, errors.New("No such image")
		}
		if args[2] == plan.Image {
			return []byte(loadedID + "\n"), nil
		}
		return nil, errors.New("unexpected inspect")
	}
	got, err := c.resolveArtifactImage(context.Background(), plan)
	if err != nil || got != plan.Image {
		t.Fatalf("loaded name: got %q err=%v", got, err)
	}
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if args[2] == pinned {
			return []byte(loadedID + "\n"), nil
		}
		t.Fatal("name inspect should not run when pin exists")
		return nil, errors.New("unused")
	}
	got, err = c.resolveArtifactImage(context.Background(), plan)
	if err != nil || got != pinned {
		t.Fatalf("pinned: got %q err=%v", got, err)
	}
	if !artifactImageMatches(plan.Image+":latest", plan, plan.Image) || !artifactImageMatches(pinned, plan, pinned) || artifactImageMatches("other", plan, plan.Image) {
		t.Fatal("artifact image match")
	}
}

func TestGenericDockerFallbackRejectsWindowsBridge(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	pe := filepath.Join(root, "hubctl.exe")
	if err := os.WriteFile(pe, []byte("MZ"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: pe, Definition: d, MaxActive: 1, IdleTTLSeconds: 60}); err == nil {
		t.Fatal("Windows hubctl.exe accepted as a Linux bridge")
	}
}

func TestGenericFallbackResourceNamesAreUniquePerWorkload(t *testing.T) {
	seen := map[string]string{}
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("w%02d", i)
		resources := genericFallbackResources(id)
		for _, name := range []string{resources.Network, resources.ProxyName, resources.ProxyVol, resources.BridgeVol, resources.RelayName, resources.RelayVol, resources.BridgeSeed, resources.RemoteName, genericStateVolume(id, 0)} {
			if owner, ok := seen[name]; ok {
				t.Fatalf("resource %q shared by %s and %s", name, owner, id)
			}
			seen[name] = id
		}
	}
	shared := genericFallbackResources("shared-ref")
	other := genericFallbackResources("other-ref")
	if shared.Network == other.Network || shared.BridgeVol == other.BridgeVol || genericStateVolume("shared-ref", 0) == genericStateVolume("other-ref", 0) {
		t.Fatal("distinct workloads reused fallback names")
	}
}

func TestGenericDockerFallbackRejectsStateAndCredentials(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	base := d
	for name, mutate := range map[string]func(*ToolDefinition){
		"shared-state": func(definition *ToolDefinition) {
			definition.Workload.Class = Shared
		},
		"state":       func(definition *ToolDefinition) { definition.Workload.Stateful = true },
		"credentials": func(definition *ToolDefinition) { definition.Credentials = []CredentialInput{{Name: "TOKEN"}} },
		"host-workspace-mount": func(definition *ToolDefinition) {
			definition.Execution.Mounts = []Mount{{Source: "workspace-readonly", Target: "/workspace", ReadOnly: true}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			mutate(&candidate)
			controller, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: candidate, MaxActive: 1, IdleTTLSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			controller.command = func(context.Context, string, ...string) ([]byte, error) {
				return []byte("ToolHive v0.48.0"), nil
			}
			plan := genericPlan(candidate)
			if name == "shared-state" {
				plan.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
			}
			if _, err := controller.start(context.Background(), plan); err == nil {
				t.Fatal("unsafe fallback workload accepted")
			}
		})
	}
}

func TestGenericFallbackHelpers(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("127.0.0.1:41234\n"), nil }
	if port, err := c.publishedPort(context.Background(), "relay"); err != nil || port != 41234 {
		t.Fatalf("published port: %d %v", port, err)
	}
	for _, value := range []string{"", "bad"} {
		c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte(value), nil }
		if _, err := c.publishedPort(context.Background(), "relay"); err == nil {
			t.Fatalf("invalid port mapping accepted: %q", value)
		}
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("docker port failed") }
	if _, err := c.publishedPort(context.Background(), "relay"); err == nil {
		t.Fatal("docker port failure accepted")
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("127.0.0.1:0\n"), nil }
	if _, err := c.publishedPort(context.Background(), "relay"); err == nil {
		t.Fatal("zero relay port accepted")
	}
	logRoot := filepath.Join(root, "toolhive")
	if err := os.MkdirAll(filepath.Join(logRoot, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	name := "relay-thv"
	if err := os.WriteFile(filepath.Join(logRoot, "logs", name+".log"), []byte(`{"endpoint":"http://localhost:41235/mcp"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if endpoint, err := c.toolHiveEndpoint(context.Background(), root, name); err != nil || endpoint != "http://127.0.0.1:41235/mcp" {
		t.Fatalf("log endpoint: %q %v", endpoint, err)
	}
	if err := os.WriteFile(filepath.Join(logRoot, "logs", name+".log"), []byte(`{"endpoint":"https://localhost:41235/mcp"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.toolHiveEndpoint(context.Background(), root, name); err == nil {
		t.Fatal("unsafe ToolHive endpoint accepted")
	}
	_ = os.Remove(filepath.Join(logRoot, "logs", name+".log"))
	if _, err := isolatedCommandEnv(context.Background(), map[string]string{"HUB_TEST_ENV": "ok"}, os.Args[0], "-test.run=^$"); err != nil {
		t.Fatalf("isolated command: %v", err)
	}
	environ := isolatedToolHiveEnviron(map[string]string{"HOME": "isolated-home", "USERPROFILE": "isolated-home"})
	homeHits := 0
	for _, item := range environ {
		if strings.HasPrefix(strings.ToUpper(item), "HOME=") {
			homeHits++
			if item != "HOME=isolated-home" {
				t.Fatalf("host HOME leaked into ToolHive env: %s", item)
			}
		}
	}
	if homeHits != 1 {
		t.Fatalf("HOME entries: %d %v", homeHits, environ)
	}
	c.commandEnv = func(_ context.Context, _ map[string]string, _ string, args ...string) ([]byte, error) {
		if len(args) < 1 || args[0] != "list" {
			return nil, errors.New("expected list")
		}
		return []byte(`[{"name":"listed-thv","url":"http://127.0.0.1:41236/mcp"}]`), nil
	}
	if endpoint, err := c.toolHiveEndpoint(context.Background(), root, "listed-thv"); err != nil || endpoint != "http://127.0.0.1:41236/mcp" {
		t.Fatalf("list endpoint: %q %v", endpoint, err)
	}
	profile, err := os.ReadFile(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if !securityOptHasSeccomp([]string{"seccomp=" + parts[1]}, parts[1]) || !securityOptHasSeccomp([]string{"seccomp=" + string(profile)}, parts[1]) || securityOptHasSeccomp([]string{`seccomp={"defaultAction":"SCMP_ACT_ALLOW"}`}, parts[1]) {
		t.Fatal("seccomp option comparison broken")
	}
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary == "docker" && len(args) > 0 && args[0] == "context" {
			return []byte("npipe://./pipe/dockerDesktopLinuxEngine"), nil
		}
		return []byte("linux"), nil
	}
	if err := c.requireLocalLinuxDocker(context.Background()); err != nil {
		t.Fatalf("local docker preflight: %v", err)
	}
	c.command = func(_ context.Context, binary string, _ ...string) ([]byte, error) {
		if binary == "docker" {
			return []byte("windows"), nil
		}
		return nil, nil
	}
	if err := c.requireLocalLinuxDocker(context.Background()); err == nil {
		t.Fatal("non-linux Docker runtime accepted")
	}
	c.command = func(_ context.Context, binary string, _ ...string) ([]byte, error) {
		if binary == "docker" {
			return []byte("tcp://remote"), nil
		}
		return nil, nil
	}
	if err := c.requireLocalLinuxDocker(context.Background()); err == nil {
		t.Fatal("remote docker context accepted")
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("docker unavailable")
	}
	if err := c.requireLocalLinuxDocker(context.Background()); err == nil {
		t.Fatal("docker command failure accepted")
	}
	c.config.Definition.Source.Command = ""
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"Entrypoint":["/app/server"],"Cmd":["--stdio"]}`), nil
	}
	if command, err := c.artifactCommand(context.Background(), d.Source.Image); err != nil || !reflect.DeepEqual(command, []string{"/app/server", "--stdio"}) {
		t.Fatalf("image command: %v %v", command, err)
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte("bad"), nil }
	if _, err := c.artifactCommand(context.Background(), d.Source.Image); err == nil {
		t.Fatal("invalid image config accepted")
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("image unavailable") }
	if _, err := c.artifactCommand(context.Background(), d.Source.Image); err == nil {
		t.Fatal("image inspect failure accepted")
	}
	c.config.Definition.Source.Command = "sh"
	if _, err := c.artifactCommand(context.Background(), d.Source.Image); err == nil {
		t.Fatal("unsafe source entrypoint accepted")
	}
	c.commandEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) {
		return []byte(`[]`), nil
	}
	if _, err := c.toolHiveEndpoint(context.Background(), root, "missing"); err == nil {
		t.Fatal("missing ToolHive endpoint accepted")
	}
	c.commandEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) {
		return []byte("not-json"), nil
	}
	if _, err := c.toolHiveEndpoint(context.Background(), root, "missing"); err == nil {
		t.Fatal("invalid ToolHive endpoint accepted")
	}
	c.config.Definition.Source.Command = "/app/server"
	c.config.Definition.Source.Args = []string{"--stdio"}
	if command, err := c.artifactCommand(context.Background(), d.Source.Image); err != nil || !reflect.DeepEqual(command, []string{"/app/server", "--stdio"}) {
		t.Fatalf("source command: %v %v", command, err)
	}
	for _, command := range [][]string{nil, {"sh", "-c", "echo"}, {"/app/server", "${TOKEN"}, {"/app/server", "line\nfeed"}} {
		if _, err := validateArtifactCommand(command); err == nil {
			t.Fatalf("unsafe command accepted: %#v", command)
		}
	}
	mountPlan := genericPlan(d)
	mountPlan.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
	workload := genericWorkload{plan: mountPlan, bridgeVol: "bridge", stateVols: []string{"state"}}
	validMounts := []genericMount{{Type: "volume", Name: "state", Destination: "/state", RW: true}, {Type: "volume", Name: "bridge", Destination: "/hermes-bridge", RW: false}}
	if !fallbackBridgeMountsOK(validMounts, workload) {
		t.Fatal("valid fallback mounts rejected")
	}
	for _, mounts := range [][]genericMount{
		validMounts[:1],
		append(append([]genericMount(nil), validMounts...), genericMount{Type: "volume", Name: "extra", Destination: "/extra"}),
		{{Type: "bind", Name: "state", Destination: "/state", RW: true}, validMounts[1]},
		{{Type: "volume", Name: "state", Destination: "/wrong", RW: true}, validMounts[1]},
		{{Type: "volume", Name: "state", Destination: "/state", RW: false}, validMounts[1]},
		{{Type: "volume", Name: "state", Destination: "/state", RW: true}, {Type: "volume", Name: "other", Destination: "/hermes-bridge"}},
	} {
		if fallbackBridgeMountsOK(mounts, workload) {
			t.Fatalf("unsafe fallback mounts accepted: %#v", mounts)
		}
	}
	stagedPath := filepath.Join(root, "staged.json")
	if err := NewStore().Save(stagedPath); err != nil {
		t.Fatal(err)
	}
	staged, err := LoadStaged(stagedPath)
	if err != nil || staged.path != "" {
		t.Fatalf("staged store load: %v", err)
	}
}

func TestGenericDockerFallbackFailureBoundaries(t *testing.T) {
	for name, fail := range map[string]func(string, []string) bool{
		"toolhive-version": func(binary string, args []string) bool {
			return binary != "docker" && len(args) > 0 && args[0] == "version"
		},
		"network-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "network" && args[1] == "create"
		},
		"proxy-volume-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "volume" && args[1] == "create" && strings.Contains(args[len(args)-1], "-egress-config")
		},
		"bridge-volume-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "volume" && args[1] == "create" && strings.HasSuffix(args[len(args)-1], "-bridge")
		},
		"relay-volume-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "volume" && args[1] == "create" && strings.HasSuffix(args[len(args)-1], "-relay-bin")
		},
		"proxy-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "create" && strings.Contains(args[2], "-egress")
		},
		"proxy-copy": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "cp" && strings.Contains(args[2], "-egress")
		},
		"proxy-connect": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 3 && args[0] == "network" && args[1] == "connect" && strings.Contains(args[3], "-egress")
		},
		"proxy-start": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "start" && strings.Contains(args[1], "-egress")
		},
		"proxy-inspect": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "inspect" && strings.Contains(args[1], "-egress") && contains(args, "--format")
		},
		"seed-copy": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "cp" && strings.Contains(args[2], "bridge-seed")
		},
		"seed-remove": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "rm" && args[1] != "--force" && strings.Contains(args[1], "bridge-seed")
		},
		"bridge-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "create" && args[2] == "generic-workload"
		},
		"bridge-start": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "start" && !strings.Contains(args[1], "-egress") && !strings.Contains(args[1], "-relay")
		},
		"relay-create": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 2 && args[0] == "create" && strings.Contains(args[2], "-relay")
		},
		"relay-connect": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 3 && args[0] == "network" && args[1] == "connect" && strings.Contains(args[3], "-relay")
		},
		"relay-start": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "start" && strings.Contains(args[1], "-relay")
		},
		"relay-port": func(binary string, args []string) bool {
			return binary == "docker" && len(args) > 1 && args[0] == "port"
		},
		"toolhive-run": func(binary string, args []string) bool {
			return binary != "docker" && len(args) > 1 && args[0] == "run" && strings.HasPrefix(args[1], "http://")
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, root, paths := genericDefinition(t)
			parts := strings.Split(paths, "\x00")
			controller, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			plan := genericPlan(d)
			containers, _ := json.Marshal(genericContainers(plan, parts[1], plan.WorkloadID+"-egress-config"))
			var failed bool
			controller.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
				if !failed && fail(binary, args) {
					failed = true
					return nil, errors.New("injected fallback failure")
				}
				if binary != "docker" && len(args) > 0 && args[0] == "version" {
					return []byte("ToolHive v0.49.0"), nil
				}
				if binary != "docker" && len(args) > 0 && args[0] == "run" {
					return []byte("--read-only"), nil
				}
				if binary == "docker" && len(args) > 0 && args[0] == "context" {
					return []byte("npipe://./pipe/dockerDesktopLinuxEngine"), nil
				}
				if binary == "docker" && len(args) > 0 && args[0] == "info" {
					return []byte("linux"), nil
				}
				if binary == "docker" && len(args) > 0 && args[0] == "inspect" && contains(args, "--format") {
					return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
				}
				if binary == "docker" && len(args) > 0 && args[0] == "inspect" && len(args) >= 3 {
					return containers, nil
				}
				if binary == "docker" && len(args) > 0 && args[0] == "port" {
					return []byte("127.0.0.1:40001\n"), nil
				}
				return nil, nil
			}
			controller.commandEnv = func(_ context.Context, environment map[string]string, binary string, args ...string) ([]byte, error) {
				if !failed && fail(binary, args) {
					failed = true
					return nil, errors.New("injected fallback failure")
				}
				if len(args) > 0 && args[0] == "run" {
					logDir := filepath.Join(environment["HOME"], "toolhive", "logs")
					if err := os.MkdirAll(logDir, 0700); err != nil {
						return nil, err
					}
					if err := os.WriteFile(filepath.Join(logDir, "generic-workload-thv.log"), []byte(`{"endpoint":"http://localhost:40002/mcp"}`), 0600); err != nil {
						return nil, err
					}
				}
				return nil, nil
			}
			if _, err := controller.start(context.Background(), plan); err == nil {
				t.Fatal("injected fallback failure was accepted")
			}
		})
	}
}
