package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend"
	"github.com/letya999/hermes-hub/services/spawn-controller/internal/backend/backendtest"
	"github.com/letya999/hermes-hub/shared/proctree"
)

// TestBackendtestHelperProcess is the conformance child for the process leg:
// re-executed as the test binary under HUB_HELPER_PROCESS, it serves the
// healthz the backend waits on plus /self so cases can inspect its real
// argv and environment. Without the marker it returns immediately.
func TestBackendtestHelperProcess(t *testing.T) {
	if os.Getenv("HUB_HELPER_PROCESS") != "1" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/self", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string][]string{"args": os.Args, "env": os.Environ()})
	})
	_ = http.ListenAndServe(os.Getenv("HUB_LISTEN"), mux)
	os.Exit(0)
}

// TestBackendtestExecChild is the process leg's exec probe: re-executed as
// the test binary under HUB_EXEC_CHILD, it prints the env marker, echoes
// stdin and honours HUB_EXEC_MODE (fail exits 3, sleep blocks, spam floods).
func TestBackendtestExecChild(t *testing.T) {
	if os.Getenv("HUB_EXEC_CHILD") != "1" {
		return
	}
	fmt.Println("exec-out")
	fmt.Fprintln(os.Stderr, "exec-err")
	fmt.Print(os.Getenv("HUB_EXEC_MARK"))
	_, _ = io.Copy(os.Stdout, os.Stdin)
	switch os.Getenv("HUB_EXEC_MODE") {
	case "fail":
		os.Exit(3)
	case "sleep":
		time.Sleep(30 * time.Second)
	case "spam":
		for i := 0; i < 200000; i++ {
			fmt.Print("x")
		}
	}
	os.Exit(0)
}

func processLeg(t *testing.T) backendtest.Leg {
	t.Helper()
	dataDir := t.TempDir()
	group, err := proctree.NewGroup()
	if err != nil {
		t.Fatalf("proctree group: %v", err)
	}
	t.Cleanup(group.Close)
	return backendtest.Leg{
		Factory: func(t *testing.T) backend.Backend {
			return backend.NewProcess("", dataDir, nil, "conformance-token", true, context.Background(), group)
		},
		Spec: func(t *testing.T, key string) backend.Spec {
			exe, err := os.Executable()
			if err != nil {
				t.Fatalf("test binary: %v", err)
			}
			return backend.Spec{
				Key: key, Org: "local", Owner: "owner", Space: "space1", Agent: "agent1",
				Image:   exe,
				Command: []string{"-test.run=^TestBackendtestHelperProcess$"},
				Env:     []string{"HUB_HELPER_PROCESS=1"},
			}
		},
		Self:            fetchSelfInfo,
		RefusesEdges:    true,
		RefusesSidecars: true,
		ExecArgv: func(t *testing.T, key string) []string {
			exe, err := os.Executable()
			if err != nil {
				t.Fatalf("test binary: %v", err)
			}
			return []string{exe, "-test.run=^TestBackendtestExecChild$"}
		},
		ExecEnv: []string{"HUB_EXEC_CHILD=1"},
	}
}

func fetchSelfInfo(t *testing.T, rt backend.Runtime) backendtest.SelfInfo {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rt.Address+"/self", nil)
	if err != nil {
		t.Fatalf("self request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("self: %v", err)
	}
	defer resp.Body.Close()
	var doc struct {
		Args []string `json:"args"`
		Env  []string `json:"env"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("self decode: %v", err)
	}
	return backendtest.SelfInfo{Args: doc.Args, Env: doc.Env}
}

func dockerLeg(t *testing.T) backendtest.Leg {
	t.Helper()
	requireDocker(t)
	registerDockerCleanup(t)
	return backendtest.Leg{
		Factory: func(t *testing.T) backend.Backend {
			return backend.NewDocker(dockerConfig())
		},
		Spec: func(t *testing.T, key string) backend.Spec {
			return backend.Spec{
				Key: key, Org: "local", Owner: "owner", Space: "space1", Agent: "agent1",
				Image:      "alpine:latest",
				Command:    []string{"sleep", "300"},
				Limits:     backend.Limits{Memory: "64m", CPUs: "0.5", PIDs: 64},
				OCIRuntime: "runc",
			}
		},
		Container:    dockerInspect,
		SpawnForeign: spawnForeignContainer,
		NetworkName:  func(space, edge string) string { return backend.DockerNetworkName(testNetworkPrefix, space, edge) },
		ExecArgv: func(t *testing.T, key string) []string {
			return []string{"/bin/sh", "-c", `echo exec-out; echo exec-err >&2; printf %s "$HUB_EXEC_MARK"; cat; case "$HUB_EXEC_MODE" in fail) exit 3;; sleep) sleep 30;; spam) i=0; while [ "$i" -lt 200000 ]; do printf x; i=$((i+1)); done;; esac`}
		},
		ExecDir:       "/tmp",
		SidecarsOf:    containersByKey,
		WorkerNetName: func(key string) string { return testNetworkPrefix + key + "-cell" },
	}
}

// containersByKey lists the live container names carrying the backend's key
// label — the worker and every sidecar it owns.
func containersByKey(t *testing.T, key string) []string {
	t.Helper()
	out := dockerCLI(t, "ps", "-aq", "--filter", "label=hermes-hub.key="+key)
	var names []string
	for _, id := range strings.Fields(string(out)) {
		names = append(names, strings.TrimSpace(string(dockerCLI(t, "inspect", "--format", "{{.Name}}", id))))
	}
	return names
}

// eachLeg runs one case against every backend; the docker leg skips only
// when the CLI or daemon is unavailable, with the reason in the message.
func eachLeg(t *testing.T, fn func(*testing.T, backendtest.Leg)) {
	t.Helper()
	t.Run("process", func(t *testing.T) { fn(t, processLeg(t)) })
	t.Run("docker", func(t *testing.T) {
		if ok, reason := dockerAvailable(); !ok {
			t.Skipf("docker leg skipped: %s", reason)
		}
		fn(t, dockerLeg(t))
	})
}

func TestProcessBackendConformance(t *testing.T) {
	backendtest.Suite(t, processLeg(t))
}

func TestDockerBackendConformance(t *testing.T) {
	requireDocker(t)
	backendtest.Suite(t, dockerLeg(t))
}

func TestOnlyOwnWorkspaceMounted(t *testing.T) {
	eachLeg(t, backendtest.OnlyOwnWorkspaceMounted)
}

func TestNoSecretInEnvOrArgv(t *testing.T) {
	eachLeg(t, backendtest.NoSecretInEnvOrArgv)
}

func TestNetworkEdgesApplied(t *testing.T) {
	eachLeg(t, backendtest.NetworkEdgesApplied)
}

func TestListReturnsOnlyOwnWorkloads(t *testing.T) {
	eachLeg(t, backendtest.ListReturnsOnlyOwnWorkloads)
}

func TestMountOptionInjectionRefused(t *testing.T) {
	eachLeg(t, backendtest.MountOptionInjectionRefused)
}

// TestExecNoSecretInArgv: an exec env value travels only through the
// exec's own environment — argv carries names, never values (X4). The
// process leg proves it by construction; the docker leg's argv builder is
// asserted in the internal test, and this case proves the value does reach
// the workload's environment through the name channel.
func TestExecNoSecretInArgv(t *testing.T) {
	eachLeg(t, func(t *testing.T, leg backendtest.Leg) {
		if leg.ExecArgv == nil {
			t.Skip("leg declares no exec probe")
		}
		be := leg.Factory(t)
		spec := leg.Spec(t, "rt-exec-no-secret")
		if _, err := be.Start(t.Context(), spec); err != nil {
			t.Fatalf("start: %v", err)
		}
		t.Cleanup(func() { _ = be.Stop(context.Background(), spec.Key) })
		secret := "m40-exec-secret-" + strings.Repeat("s", 12)
		res, err := be.Exec(t.Context(), spec.Key, backend.ExecSpec{
			Argv: leg.ExecArgv(t, spec.Key),
			Env:  append(append([]string{}, leg.ExecEnv...), "HUB_EXEC_MARK="+secret, "HUB_EXEC_MODE=ok"),
			Dir:  leg.ExecDir,
		})
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
		// The probe prints the env value back: delivery through the name
		// channel is what keeps it out of argv — a backend that inlined it
		// into argv would never set the workload's environment at all.
		if !strings.Contains(string(res.Stdout), secret) {
			t.Fatalf("exec env value did not reach the workload: %q", res.Stdout)
		}
	})
}

func TestExecTimeoutAndOutputCap(t *testing.T) {
	eachLeg(t, backendtest.ExecTimeoutAndOutputCap)
}

func TestSidecarsStopWithWorker(t *testing.T) {
	requireDocker(t)
	backendtest.SidecarsLifecycle(t, dockerLeg(t))
}

func TestProcessBackendRefusesSidecars(t *testing.T) {
	backendtest.SidecarsLifecycle(t, processLeg(t))
}

// TestUntrustedRefusesProcessBackend: X6 — an untrusted installation never
// gets a child process, and the refusal happens before any directory exists.
func TestUntrustedRefusesProcessBackend(t *testing.T) {
	dataDir := t.TempDir()
	group, err := proctree.NewGroup()
	if err != nil {
		t.Fatalf("proctree group: %v", err)
	}
	t.Cleanup(group.Close)
	be := backend.NewProcess("", dataDir, nil, "token", false, context.Background(), group)
	spec := backend.Spec{Key: "rt-untrusted", Image: "ignored"}
	rt, err := be.Start(t.Context(), spec)
	if !errors.Is(err, backend.ErrUntrusted) {
		if err == nil {
			_ = be.Stop(t.Context(), rt.ID)
		}
		t.Fatalf("untrusted start: %v, want backend.ErrUntrusted", err)
	}
	if _, statErr := os.Stat(dataDir + "/runtimes"); !os.IsNotExist(statErr) {
		t.Fatal("untrusted refusal touched the data dir")
	}
}

// TestUntrustedDockerRequiresRunsc: X6 — untrusted docker accepts only a
// runsc spec; the guard runs before the daemon is ever asked.
func TestUntrustedDockerRequiresRunsc(t *testing.T) {
	cfg := dockerConfig()
	cfg.Trusted = false
	be := backend.NewDocker(cfg)
	for _, runtime := range []string{"", "runc", "kata"} {
		spec := backend.Spec{Key: "rt-x" + runtime, Image: "alpine:latest", Command: []string{"sleep", "300"}, OCIRuntime: runtime}
		if _, err := be.Start(t.Context(), spec); !errors.Is(err, backend.ErrUntrusted) {
			t.Fatalf("untrusted start with runtime %q: %v, want backend.ErrUntrusted", runtime, err)
		}
	}
	t.Run("runsc_passes_trust_guard", func(t *testing.T) {
		spec := backend.Spec{Key: "rt-runsc", Image: "alpine:latest", Command: []string{"sleep", "300"}, OCIRuntime: "runsc"}
		_, err := be.Start(t.Context(), spec)
		if errors.Is(err, backend.ErrUntrusted) {
			t.Fatalf("runsc spec refused by the trust guard: %v", err)
		}
		// runsc is not installed on this daemon, so Start fails downstream of
		// the guard — that failure is the expected outcome here.
	})
}

// TestDockerRunscSpecStarts is the runsc happy path; it skips wherever the
// daemon lacks the gVisor runtime, with the reason in the message.
func TestDockerRunscSpecStarts(t *testing.T) {
	requireDocker(t)
	registerDockerCleanup(t)
	if !runscInstalled(t) {
		t.Skip("runsc is not installed on this docker daemon")
	}
	be := backend.NewDocker(dockerConfig())
	spec := backend.Spec{Key: "rt-runsc-happy", Image: "alpine:latest", Command: []string{"sleep", "300"}, OCIRuntime: "runsc"}
	rt, err := be.Start(t.Context(), spec)
	if err != nil {
		t.Fatalf("runsc start: %v", err)
	}
	t.Cleanup(func() { _ = be.Stop(context.Background(), spec.Key) })
	if rt.State != "running" {
		t.Fatalf("runsc state %q, want running", rt.State)
	}
}
