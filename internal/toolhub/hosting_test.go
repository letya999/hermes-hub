package toolhub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/hosting"
)

func siteFixture(t *testing.T) (*genericController, hosting.Workload, HostingProfile, map[string]any) {
	t.Helper()
	root := t.TempDir()
	p := HostingProfile{Owner: "alice", Environment: "dev", Volume: "sites-alice-dev", Image: "golang@sha256:" + strings.Repeat("a", 64), Runtime: "runsc"}
	w := hosting.Workload{Binding: hosting.Binding{Owner: "alice", Environment: "dev"}, Name: "page", Revision: 3, Release: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Hour)}
	c := &genericController{config: GenericControllerConfig{HostingFleetRoot: t.TempDir(), Hosting: map[string]HostingProfile{"alice/dev": p}, HostingHelperVolume: "helpers", HostingCellInit: filepath.Join(root, "cellinit"), SeccompProfile: filepath.Join(root, "seccomp")}, runtimes: map[string]bool{"runsc": true}, runtimesOK: true}
	v := map[string]any{
		"State":           map[string]any{"Running": true},
		"NetworkSettings": map[string]any{"Networks": map[string]any{"none": map[string]any{}}},
		"Config":          map[string]any{"Image": p.Image, "User": "10001:10001", "WorkingDir": "/work", "Entrypoint": []string{c.siteInitHost()}, "Cmd": []string{"site-go", strconv.FormatInt(w.ExpiresAt.Unix(), 10)}, "Env": []string{"PATH=/usr/local/bin:/usr/bin:/bin"}, "Labels": map[string]string{"hermes-hub.owner": "alice", "hermes-hub.site": "page", "hermes-hub.site.env": "dev", "hermes-hub.site.revision": "3", "hermes-hub.site.release": w.Release}},
		"HostConfig":      map[string]any{"NanoCpus": int64(1e9), "Memory": 256 << 20, "MemorySwap": 256 << 20, "PidsLimit": 64, "ReadonlyRootfs": true, "NetworkMode": "none", "Runtime": "runsc", "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges=true", "seccomp=" + c.config.SeccompProfile}, "Tmpfs": map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=32m"}, "LogConfig": map[string]string{"Type": "none"}, "RestartPolicy": map[string]string{"Name": "no"}, "Mounts": []map[string]any{{"Type": "volume", "Source": p.Volume, "Target": "/work", "ReadOnly": true, "VolumeOptions": map[string]any{"Subpath": w.Release}}, {"Type": "volume", "Source": "helpers", "Target": "/cell-tools", "ReadOnly": true}}},
		"Mounts":          []map[string]any{{"Type": "volume", "Name": p.Volume, "Destination": "/work", "RW": false}, {"Type": "volume", "Name": "helpers", "Destination": "/cell-tools", "RW": false}},
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { return json.Marshal([]any{v}) }
	return c, w, p, v
}
func TestHostingInspectDriftDenied(t *testing.T) {
	cases := map[string]func(map[string]any){
		"devices": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Devices"] = []map[string]string{{"PathOnHost": "/dev/mem"}}
		},
		"published-port": func(v map[string]any) { v["HostConfig"].(map[string]any)["PublishAllPorts"] = true },
		"extra-network": func(v map[string]any) {
			v["NetworkSettings"] = map[string]any{"Networks": map[string]any{"none": map[string]any{}, "hub": map[string]any{}}}
		},
		"extra-tmpfs": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Tmpfs"].(map[string]string)["/state"] = "rw,size=32m"
		},

		"root":        func(v map[string]any) { v["Config"].(map[string]any)["User"] = "0" },
		"credentials": func(v map[string]any) { v["Config"].(map[string]any)["Env"] = []string{"SECRET=forbidden"} },
		"runtime":     func(v map[string]any) { v["HostConfig"].(map[string]any)["Runtime"] = "runc" },
		"writable":    func(v map[string]any) { v["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"network":     func(v map[string]any) { v["HostConfig"].(map[string]any)["NetworkMode"] = "host" },
		"memory":      func(v map[string]any) { v["HostConfig"].(map[string]any)["Memory"] = 0 },
		"caps":        func(v map[string]any) { v["HostConfig"].(map[string]any)["CapAdd"] = []string{"SYS_ADMIN"} },
		"logs": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["LogConfig"] = map[string]string{"Type": "json-file"}
		},
		"full-store": func(v map[string]any) {
			v["HostConfig"].(map[string]any)["Mounts"].([]map[string]any)[0]["VolumeOptions"] = map[string]any{"Subpath": ""}
		},
		"foreign-volume": func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["Name"] = "bob-sites" },
		"extra-mount": func(v map[string]any) {
			v["Mounts"] = append(v["Mounts"].([]map[string]any), map[string]any{"Type": "bind", "Destination": "/state"})
		},
	}
	c, w, p, _ := siteFixture(t)
	if err := c.siteProfile(context.Background(), w, p, true); err != nil {
		t.Fatalf("valid profile: %v", err)
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, w, p, v := siteFixture(t)
			mutate(v)
			if err := c.siteProfile(context.Background(), w, p, true); !errors.Is(err, hosting.ErrDenied) {
				t.Fatalf("drift accepted: %v", err)
			}
		})
	}
}
func TestHostingMissingSecureRuntimeDenied(t *testing.T) {
	c, w, _, _ := siteFixture(t)
	for _, runtime := range []string{"", "runc", "unknown"} {
		p := c.config.Hosting["alice/dev"]
		p.Runtime = runtime
		c.config.Hosting["alice/dev"] = p
		if _, err := c.hostingProfile(w); err == nil {
			t.Fatalf("runtime %q admitted", runtime)
		}
	}
	c, w, p, _ := siteFixture(t)
	c.runtimes = map[string]bool{}
	called := false
	c.command = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	if err := c.siteEnsure(context.Background(), w, p); err == nil || called {
		t.Fatal("missing runtime reached daemon create")
	}
}
func TestHostingControllerCreateInspectStart(t *testing.T) {
	c, w, p, v := siteFixture(t)
	exists, running, started := false, false, false
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			if !exists {
				return nil, errors.New("absent")
			}
			v["State"] = map[string]bool{"Running": running}
			return json.Marshal([]any{v})
		case "info":
			return []byte("4 8589934592"), nil
		case "ps":
			return nil, nil
		case "volume":
			return []byte("[{\"Labels\":{\"hermes-hub.hosting.owner\":\"alice\",\"hermes-hub.hosting.env\":\"dev\"}}]"), nil
		case "create":
			joined := strings.Join(args, " ")
			for _, want := range []string{"--network none", "--runtime runsc", "--log-driver none", "volume-subpath=" + w.Release, "--read-only", "--restart no"} {
				if !strings.Contains(joined, want) {
					return nil, fmt.Errorf("missing mandatory create option")
				}
			}
			exists = true
			return nil, nil
		case "start":
			started = true
			running = true
			return nil, nil
		case "exec":
			return nil, nil
		case "rm":
			exists = false
			running = false
			return nil, nil
		}
		return nil, errors.New("unexpected daemon operation")
	}
	c.commandStdin = func(_ context.Context, in io.Reader, _ string, args ...string) ([]byte, error) {
		if !started {
			t.Error("relay before start")
		}
		body, _ := io.ReadAll(in)
		if !strings.Contains(string(body), "\"path\":\"/\"") {
			t.Error("readiness path")
		}
		return []byte("{\"status\":200,\"body\":\"\"}"), nil
	}
	if err := c.siteEnsure(context.Background(), w, p); err != nil || !started {
		t.Fatalf("admission %v started=%t", err, started)
	}
	if err := c.siteRemove(context.Background(), w, p); err != nil || exists {
		t.Fatalf("cleanup %v exists=%t", err, exists)
	}
}
func TestHostingControllerAuthorizationAndOnlyHostingSurface(t *testing.T) {
	c, workload, _, _ := siteFixture(t)
	c.config.HostingOnly = true
	token := strings.Repeat("t", 32)
	handler := c.handler(token)
	for _, tc := range []struct {
		path, token, body string
		status            int
	}{
		{"/hosting", "", "{}", 401}, {"/admit", token, "{}", 404}, {"/hosting", token, "{} trailing", 403},
	} {
		r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s got %d", tc.path, w.Code)
		}
	}
	workload.Binding.Owner = "bob"
	body, _ := json.Marshal(hosting.ExecutorRequest{Action: "ensure", Workload: workload})
	r := httptest.NewRequest("POST", "/hosting", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-owner %d", w.Code)
	}
}

func TestHostingRelayCommandOutputBoundedAndPrivate(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "site-output-child" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		_, _ = io.WriteString(os.Stderr, "synthetic-private-stderr")
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), (12<<20)+1))
		os.Exit(0)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	output, err := siteCommandInput(context.Background(), strings.NewReader("request"), binary, "-test.run=^TestHostingRelayCommandOutputBoundedAndPrivate$", "--", "site-output-child")
	if err == nil || len(output) > 12<<20 || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("unbounded or secret-bearing relay command result")
	}
}
