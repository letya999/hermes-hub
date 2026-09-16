package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func localFixture(t *testing.T) *localController {
	t.Helper()
	d := statefulContainerDefinition()
	d.Source.Command = ""
	d.Execution.Egress = append([]string(nil), telegramIPv4...)
	d.Execution.Mounts = []Mount{{Source: "connection-state", Target: "/run/connector"}}
	c, err := newLocalController(LocalControllerConfig{User: "alice", Context: "work", Connection: "telegram-read-1", StateRoot: t.TempDir(), ToolHiveBinary: filepath.Join(t.TempDir(), "thv"), MCPPort: 8546, Definition: d})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func localInspectFixture(c *localController) []localContainer {
	containers := make([]localContainer, 2)
	for i := range containers {
		containers[i].State.Running = true
		containers[i].HostConfig.NanoCpus = int64(c.plan.Execution.CPUMillis) * 1000000
		containers[i].HostConfig.Memory = int64(c.plan.Execution.MemoryMiB) * 1048576
		containers[i].HostConfig.PidsLimit = c.plan.Execution.MaxPIDs
		containers[i].NetworkSettings.Networks = map[string]struct{ IPAddress string }{"hermes-" + c.plan.WorkloadID: {IPAddress: "172.20.0.2"}}
	}
	containers[0].Config.User = "10001:10001"
	containers[0].Image, containers[1].Image = c.plan.Digest, "proxy-image-id"
	containers[1].NetworkSettings.Networks["bridge"] = struct{ IPAddress string }{IPAddress: "172.17.0.2"}
	for i, target := range []string{"/run/connector", "/etc/squid/squid.conf"} {
		source := c.plan.WorkspacePath
		if i == 1 {
			source = filepath.Join(c.config.StateRoot, "telegram-proxy.conf")
		}
		containers[i].Mounts = append(containers[i].Mounts, struct {
			Type, Source, Destination string
			RW                        bool
		}{Type: "bind", Source: source, Destination: target, RW: i == 0})
	}
	return containers
}

func installLocalInspect(t *testing.T, c *localController, containers []localContainer, networkInternal bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.config.StateRoot, "telegram-proxy.conf"), []byte(c.proxyConfig), 0600); err != nil {
		t.Fatal(err)
	}
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			return json.Marshal(containers)
		case "image":
			return []byte("proxy-image-id\n"), nil
		case "network":
			return json.Marshal([]map[string]bool{{"Internal": networkInternal}})
		}
		return nil, errors.New("unexpected command")
	}
}

func TestLocalControllerPlanAndAuthorization(t *testing.T) {
	c := localFixture(t)
	if !strings.Contains(c.plan.WorkspacePath, filepath.Join("alice", "work", "telegram-account-read")) {
		t.Fatal(c.plan)
	}
	for _, mutate := range []func(*LocalControllerConfig){func(x *LocalControllerConfig) { x.User = "../bob" }, func(x *LocalControllerConfig) { x.StateRoot = "relative" }, func(x *LocalControllerConfig) { x.MCPPort = 80 }, func(x *LocalControllerConfig) { x.Definition.Execution.Egress = []string{"0.0.0.0/0"} }, func(x *LocalControllerConfig) { x.Definition.Execution.Mounts = nil }, func(x *LocalControllerConfig) { x.Definition.Workload.ToolHiveVersion = "v0.49.0" }} {
		config := c.config
		mutate(&config)
		if _, err := newLocalController(config); err == nil {
			t.Fatal("bad config accepted")
		}
	}
	installLocalInspect(t, c, localInspectFixture(c), true)
	token := strings.Repeat("x", 32)
	body, _ := json.Marshal(c.plan)
	for _, tc := range []struct {
		method, path, auth, origin, body string
		status                           int
	}{
		{"GET", "/admit", "", "", "", 404}, {"POST", "/bad", token, "", string(body), 404},
		{"POST", "/admit", "bad", "", string(body), 401}, {"POST", "/admit", token, "http://evil.test", string(body), 401},
		{"POST", "/admit", token, "", "{}", 403}, {"POST", "/admit", token, "", string(body) + "{}", 403},
		{"POST", "/admit", token, "", `{"unknown":1}`, 403}, {"POST", "/admit", token, "", strings.Repeat("x", 65537), 403},
		{"POST", "/admit", token, "", string(body), 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.auth)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		c.handler(token).ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("down") }
	r := httptest.NewRequest("POST", "/admit", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	c.handler(token).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	for _, listen := range []string{"0.0.0.0:8544", "localhost:8544", "bad"} {
		if RunLocalController(context.Background(), c.config, listen, token) == nil {
			t.Fatal("non-loopback controller accepted")
		}
	}
	if RunLocalController(context.Background(), c.config, "127.0.0.1:8544", "short") == nil {
		t.Fatal("weak auth accepted")
	}
}

func TestLocalControllerDockerProof(t *testing.T) {
	c := localFixture(t)
	installLocalInspect(t, c, localInspectFixture(c), true)
	if receipt, err := c.inspect(context.Background()); err != nil || !receipt.Enforced {
		t.Fatal(receipt, err)
	}
	for _, mutate := range []func([]localContainer){
		func(x []localContainer) { x[0].State.Running = false }, func(x []localContainer) { x[1].HostConfig.Privileged = true },
		func(x []localContainer) { x[0].HostConfig.Memory++ }, func(x []localContainer) { x[0].Image = "wrong" },
		func(x []localContainer) { x[0].Config.User = "root" }, func(x []localContainer) { x[1].Image = "wrong" },
		func(x []localContainer) { x[0].NetworkSettings.Networks["bridge"] = struct{ IPAddress string }{} },
		func(x []localContainer) { delete(x[1].NetworkSettings.Networks, "hermes-"+c.plan.WorkloadID) },
		func(x []localContainer) { x[0].Mounts[0].Source = filepath.Dir(c.plan.WorkspacePath) },
		func(x []localContainer) { x[0].Mounts[0].RW = false }, func(x []localContainer) { x[0].Mounts = nil },
		func(x []localContainer) { x[1].Mounts = append(x[1].Mounts, x[1].Mounts[0]) },
	} {
		containers := localInspectFixture(c)
		mutate(containers)
		installLocalInspect(t, c, containers, true)
		if _, err := c.inspect(context.Background()); err == nil {
			t.Fatal("bad enforcement accepted")
		}
	}
	installLocalInspect(t, c, localInspectFixture(c), false)
	if _, err := c.inspect(context.Background()); err == nil {
		t.Fatal("external network accepted")
	}
	installLocalInspect(t, c, localInspectFixture(c), true)
	if err := os.WriteFile(filepath.Join(c.config.StateRoot, "telegram-proxy.conf"), []byte("http_access allow all"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.inspect(context.Background()); err == nil {
		t.Fatal("unrestricted egress accepted")
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return []byte(`[]`), nil }
	if _, err := c.containers(context.Background()); err == nil {
		t.Fatal("missing containers accepted")
	}
}

func TestLocalControllerStartFixedCommands(t *testing.T) {
	for failAt := 0; failAt <= 9; failAt++ {
		c := localFixture(t)
		ready := false
		created := false
		commands := 0
		c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
			if args[0] == "inspect" && len(args) == 3 {
				if ready {
					return json.Marshal(localInspectFixture(c))
				}
				return nil, errors.New("missing")
			}
			if args[0] == "inspect" && len(args) == 2 {
				if created {
					return []byte("exists"), nil
				}
				return nil, errors.New("missing")
			}
			if ready && args[0] == "image" {
				return []byte("proxy-image-id"), nil
			}
			if ready && args[0] == "network" {
				return []byte(`[{"Internal":true}]`), nil
			}
			commands++
			if commands == failAt {
				return nil, errors.New("command failed")
			}
			if args[0] == "version" {
				return []byte("ToolHive v0.48.0"), nil
			}
			if args[0] == "inspect" {
				return json.Marshal(map[string]any{"hermes-" + c.plan.WorkloadID: map[string]string{"IPAddress": "172.20.0.2"}})
			}
			if args[0] == "run" {
				created = true
				count := 0
				for i, arg := range args {
					if arg == "--tools" {
						count++
						if strings.Contains(args[i+1], ",") {
							t.Fatal("ToolHive StringArray needs repeated flags")
						}
					}
				}
				if count != 5 {
					t.Fatal(args)
				}
				joined := strings.Join(args, " ")
				if !strings.Contains(joined, "--isolate-network=false") || !strings.Contains(joined, c.plan.Digest) || strings.Contains(joined, "SESSION") {
					t.Fatal(joined)
				}
			}
			if args[0] == "update" {
				ready = true
			}
			return nil, nil
		}
		err := c.start(context.Background())
		if (failAt == 0 || failAt > commands) != (err == nil) {
			t.Fatalf("failure %d commands %d: %v", failAt, commands, err)
		}
		if err == nil && c.start(context.Background()) != nil {
			t.Fatal("running workload not reused")
		}
	}
	c := localFixture(t)
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 2 {
			return []byte("exists"), nil
		}
		return nil, errors.New("bad existing")
	}
	if c.start(context.Background()) == nil {
		t.Fatal("broken existing workload replaced")
	}
	if _, err := localCommand(context.Background(), "definitely-not-a-command-hermes"); err == nil {
		t.Fatal("missing command succeeded")
	}
	if output, err := localCommand(context.Background(), "go", "version"); err != nil || !strings.Contains(string(output), "go version") {
		t.Fatal(string(output), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitLocalWorkload(ctx, func() error { return errors.New("pending") }) == nil {
		t.Fatal("readiness cancellation ignored")
	}
	attempts := 0
	if waitLocalWorkload(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return errors.New("pending")
		}
		return nil
	}) != nil || attempts != 2 {
		t.Fatal(attempts)
	}
}

func TestContainerCIDREgress(t *testing.T) {
	if noSymlinkPath(filepath.ToSlash(t.TempDir())) != nil || noSymlinkPath("relative") == nil {
		t.Fatal("path normalization boundary")
	}
	for _, host := range []string{"149.154.160.0/20", "91.108.4.0/22"} {
		d := statefulContainerDefinition()
		d.Execution.Egress = []string{host}
		if err := d.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"0.0.0.0/0", "149.154.160.1/20", "149.154.160.0/33", "2001:b28:f23d::/48", "evil/20"} {
		d := statefulContainerDefinition()
		d.Execution.Egress = []string{host}
		if d.Validate() == nil {
			t.Fatal(host)
		}
	}
}
