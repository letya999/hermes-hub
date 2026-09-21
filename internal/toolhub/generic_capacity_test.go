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
	"strings"
	"testing"
	"time"
)

func TestGenericFallbackNinetyFivePlusFiveUniqueWorkloads(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Credentials = []CredentialInput{{Name: "TOKEN", Required: true}}
	d.Workload.Stateful = true
	d.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/state"}}
	sharedDir := filepath.Join(root, "shared")
	if err := os.Mkdir(sharedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sharedDir, "credentials.env"), []byte("TOKEN=shared-ref\n"), 0600); err != nil {
		t.Fatal(err)
	}
	budget, err := NewWorkloadBudget(4, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	c.command, c.commandEnv = fakeFallbackCommands(t, d, parts[1], &calls)
	created := map[string]bool{}
	envFiles := map[string]int{}
	for i := 0; i < 100; i++ {
		plan := genericPlan(d)
		plan.WorkloadID = fmt.Sprintf("w%02d", i)
		if i >= 95 {
			plan.WorkspacePath = sharedDir
		} else {
			workspace := filepath.Join(root, plan.WorkloadID)
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte(fmt.Sprintf("TOKEN=user-%02d\n", i)), 0600); err != nil {
				t.Fatal(err)
			}
			plan.WorkspacePath = workspace
		}
		if _, err := c.start(context.Background(), plan); err != nil {
			t.Fatalf("binding %d: %v", i, err)
		}
		resources := genericFallbackResources(plan.WorkloadID)
		for _, name := range []string{resources.Network, resources.ProxyName, resources.ProxyVol, resources.BridgeVol, resources.RelayName, resources.RelayVol, resources.RemoteName, genericStateVolume(plan.WorkloadID, 0), plan.WorkloadID} {
			if created[name] {
				t.Fatalf("binding %d reused resource %s", i, name)
			}
			created[name] = true
		}
		envFiles[filepath.Join(plan.WorkspacePath, "credentials.env")]++
	}
	if len(created) != 900 {
		t.Fatalf("expected 100 unique name sets, got %d names", len(created))
	}
	if envFiles[filepath.Join(sharedDir, "credentials.env")] != 5 {
		t.Fatalf("shared credential reference uses: %v", envFiles)
	}
	joined := strings.Join(calls, "\n")
	if strings.Contains(joined, "TOKEN=shared-ref") || strings.Contains(joined, "TOKEN=user-") {
		t.Fatal("credential values entered Docker/ToolHive arguments")
	}
	if strings.Count(joined, "--env-file "+filepath.Join(sharedDir, "credentials.env")) != 5 {
		t.Fatal("shared credential reference was not reused as an env-file five times")
	}
}

func TestGenericControllerFIFOBudgetAndIdleCleanup(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	budget, err := NewWorkloadBudget(1, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, Budget: budget})
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	c.command, c.commandEnv = fakeFallbackCommands(t, d, parts[1], &calls)
	first := genericPlan(d)
	first.WorkloadID = "fifoone"
	if _, err := c.start(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), first.WorkloadID); err != nil {
		t.Fatal(err)
	}
	c.workloads[first.WorkloadID] = genericWorkload{plan: first, proxyName: first.WorkloadID + "-egress", proxyVol: first.WorkloadID + "-egress-config", dockerFallback: true}
	blocked, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := budget.Acquire(blocked, "fifotwo"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("FIFO queue did not block: %v", err)
	}
	removed := c.CleanupIdle(context.Background(), time.Now().Add(time.Second))
	if len(removed) != 1 || removed[0] != first.WorkloadID {
		t.Fatalf("idle cleanup: %v", removed)
	}
}

func fakeFallbackCommands(t *testing.T, definition ToolDefinition, seccomp string, calls *[]string) (
	func(context.Context, string, ...string) ([]byte, error),
	func(context.Context, map[string]string, string, ...string) ([]byte, error),
) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
	port := listener.Addr().(*net.TCPAddr).Port
	command := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		*calls = append(*calls, binary+" "+strings.Join(args, " "))
		if binary != "docker" && len(args) > 0 && args[0] == "version" {
			return []byte("ToolHive v0.48.0"), nil
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
			name := args[1]
			id := strings.TrimSuffix(strings.TrimSuffix(name, "-egress"), "-relay")
			return []byte(`{"hermes-` + id + `":{"IPAddress":"172.20.0.2"}}`), nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "inspect" && len(args) >= 3 {
			id := args[1]
			plan := genericPlan(definition)
			plan.WorkloadID = id
			values := genericContainers(plan, seccomp, id+"-egress-config")
			mounts := []map[string]any{{"Type": "volume", "Source": id + "-bridge", "Destination": "/hermes-bridge", "RW": false}}
			if len(plan.Execution.Mounts) > 0 {
				mounts = append([]map[string]any{{"Type": "volume", "Source": id + "-state-0", "Destination": "/state", "RW": true}}, mounts...)
			}
			values[0]["Mounts"] = mounts
			network := "hermes-" + id
			values = append(values, map[string]any{
				"Image": "relay-image-id", "State": map[string]any{"Running": true},
				"Config":          map[string]any{"User": "10001:10001", "Image": plan.SidecarImages[0], "Env": []string{"HERMES_BRIDGE_TOKEN=" + strings.Repeat("b", 32)}},
				"HostConfig":      values[0]["HostConfig"],
				"Mounts":          []map[string]any{{"Type": "volume", "Source": id + "-relay-bin", "Destination": "/hermes-relay", "RW": false}},
				"NetworkSettings": map[string]any{"Networks": map[string]any{network: map[string]string{"IPAddress": "172.20.0.4"}, "bridge": map[string]string{"IPAddress": "172.17.0.4"}}},
			})
			body, _ := json.Marshal(values)
			return body, nil
		}
		if binary == "docker" && len(args) > 0 && args[0] == "port" {
			return []byte(fmt.Sprintf("127.0.0.1:%d\n", port)), nil
		}
		return nil, nil
	}
	commandEnv := func(_ context.Context, environment map[string]string, _ string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "rm" {
			return nil, nil
		}
		if len(args) > 0 && args[0] == "run" {
			logDir := filepath.Join(environment["HOME"], "toolhive", "logs")
			if err := os.MkdirAll(logDir, 0700); err != nil {
				return nil, err
			}
			name := "workload-thv"
			for i := range args {
				if args[i] == "--name" && i+1 < len(args) {
					name = args[i+1]
				}
			}
			if err := os.WriteFile(filepath.Join(logDir, name+".log"), []byte(fmt.Sprintf(`{"endpoint":"http://localhost:%d/mcp"}`, port)), 0600); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	return command, commandEnv
}
