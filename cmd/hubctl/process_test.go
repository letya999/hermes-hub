package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/shared/locale"
)

// The helper is a foreign executable fixture for launcher lifecycle tests.
// It does not stand in for the missing minimal message path.
func TestMain(m *testing.M) {
	if os.Getenv("HUB_PROCESS_TEST_HELPER") == "1" {
		listener, err := net.Listen("tcp", os.Getenv("HUB_LISTEN"))
		if err != nil {
			os.Exit(2)
		}
		body, _ := json.Marshal(map[string]string{
			"database": os.Getenv("HUB_DATABASE_URL"), "declared": os.Getenv("DECLARED_TEST_VALUE"),
			"undeclared": os.Getenv("UNDECLARED_PROVIDER_TOKEN"), "dependency": os.Getenv("HUB_FIXTURE_ONE_URL"),
		})
		path := strings.TrimPrefix(os.Getenv("HUB_DATABASE_URL"), "sqlite://") + ".started"
		if os.WriteFile(path, body, 0o600) != nil {
			os.Exit(2)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.HandleFunc("/exit", func(w http.ResponseWriter, r *http.Request) { os.Exit(3) })
		_ = http.Serve(listener, mux)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func TestProcessModeStartsProfileWithDeclaredEnv(t *testing.T) {
	t.Setenv("HUB_PROCESS_TEST_HELPER", "1")
	t.Setenv("DECLARED_TEST_VALUE", "allowed")
	t.Setenv("UNDECLARED_PROVIDER_TOKEN", "synthetic-private")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, stop := range []string{"cancel", "first-exit"} {
		t.Run(stop, func(t *testing.T) {
			services := []ServiceDescriptor{{Name: "fixture-one", DataDir: "fixture-one.db"}, {Name: "fixture-two", DataDir: "fixture-two.db", Edges: []string{"fixture-one"}}}
			for i := range services {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				services[i].Port = listener.Addr().(*net.TCPAddr).Port
				_ = listener.Close()
				services[i].Binary = strings.TrimSuffix(filepath.Base(executable), ".exe")
				services[i].Env = []string{"HUB_PROCESS_TEST_HELPER", "DECLARED_TEST_VALUE"}
			}
			data := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- launchProcesses(ctx, services, filepath.Dir(executable), data) }()
			for i, svc := range services {
				path := filepath.Join(data, svc.DataDir) + ".started"
				var report map[string]string
				for {
					body, err := os.ReadFile(path)
					if err == nil && json.Unmarshal(body, &report) == nil {
						break
					}
					select {
					case err := <-done:
						t.Fatalf("launcher exited before start: %v", err)
					case <-ctx.Done():
						t.Fatal("profile startup timed out")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if report["declared"] != "allowed" || report["undeclared"] != "" || report["database"] != "sqlite://"+filepath.ToSlash(filepath.Join(data, svc.DataDir)) {
					t.Fatalf("incorrect launch environment for %s", svc.Name)
				}
				if i == 1 && report["dependency"] == "" {
					t.Fatal("enabled dependency was not wired")
				}
			}
			if stop == "cancel" {
				cancel()
			} else {
				client := &http.Client{Timeout: time.Second}
				resp, _ := client.Get("http://127.0.0.1:" + strconv.Itoa(services[0].Port) + "/exit")
				if resp != nil {
					_ = resp.Body.Close()
				}
			}
			select {
			case err := <-done:
				if (stop == "cancel") != (err == nil) {
					t.Fatalf("stop=%s err=%v", stop, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("launcher did not join child exits")
			}
			for _, svc := range services {
				conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(svc.Port), time.Second)
				if err == nil {
					_ = conn.Close()
					t.Fatalf("orphan child listener: %s", svc.Name)
				}
			}
		})
	}
}

func TestProcessModeRefusesUntrusted(t *testing.T) {
	t.Setenv("HUB_TRUSTED", "true")
	data := filepath.Join(t.TempDir(), "absent")
	err := runProcesses(context.Background(), []string{"--mode", "process", "--profile", "minimal", "--trusted=false", "--data", data})
	if err == nil || err.Error() != locale.T(locale.Operator(), "hubctl.run-untrusted") {
		t.Fatalf("want untrusted refusal before binary lookup, got %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("untrusted launch touched data: %v", err)
	}
}

func TestProcessModeMissingCoreDoesNotCreateData(t *testing.T) {
	data := filepath.Join(t.TempDir(), "absent")
	err := runProcesses(t.Context(), []string{"--trusted", "--bin", t.TempDir(), "--data", data})
	if err == nil || err.Error() != locale.T(locale.Operator(), "hubctl.run-missing", "spawn-controller") {
		t.Fatalf("missing core: %v", err)
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("partial profile created data: %v", err)
	}
}

func TestServiceDescriptorIsConsistent(t *testing.T) {
	names, ports := map[string]bool{}, map[int]bool{}
	services := serviceDescriptors()
	for _, svc := range services {
		if names[svc.Name] || ports[svc.Port] || svc.Binary == "" || svc.DataDir != svc.Name+".db" || svc.Port < 1024 {
			t.Fatalf("invalid service: %+v", svc)
		}
		names[svc.Name], ports[svc.Port] = true, true
		if !slices.Contains([]string{"control", "agent", "edge", "secret", "egress", "space"}, svc.Zone) || !slices.Contains([]string{"single", "multi"}, svc.ReplicaClass) {
			t.Fatalf("invalid zone/replica: %+v", svc)
		}
	}
	for _, svc := range services {
		for _, edge := range svc.Edges {
			if !names[edge] || edge == svc.Name {
				t.Fatalf("invalid edge %s -> %s", svc.Name, edge)
			}
		}
	}
	minimal := profileServices("minimal")
	if len(minimal) != 2 || minimal[0].Name != "spawn-controller" || minimal[1].Name != "harness-runtime" {
		t.Fatalf("minimal: %+v", minimal)
	}
	if len(profileServices("family")) != 9 || len(profileServices("enterprise")) != 13 {
		t.Fatal("profile membership drift")
	}
}

func TestProcessEnvironmentContainsOnlyDeclaredValues(t *testing.T) {
	t.Setenv("UNDECLARED_PROVIDER_TOKEN", "synthetic-private")
	t.Setenv("DECLARED_TEST_VALUE", "allowed")
	svc := profileServices("minimal")[1]
	svc.Env = []string{"DECLARED_TEST_VALUE"}
	env := strings.Join(processEnv(svc, profileServices("minimal"), "data"), "\n")
	if strings.Contains(env, "synthetic-private") || !strings.Contains(env, "DECLARED_TEST_VALUE=allowed") || !strings.Contains(env, "HUB_SPAWN_CONTROLLER_URL=http://127.0.0.1:9201") || strings.Contains(env, "HUB_TOOL_HUB_URL=") || !strings.Contains(env, "harness-runtime.db") {
		t.Fatalf("environment wiring: %s", env)
	}
}

func TestMinimalProfileAnswersMessage(t *testing.T) {
	t.Skip("M40.3 spawn-controller and M40.4 harness-runtime binaries are not extracted; no real minimal message path exists in M40.2")
}
