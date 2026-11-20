package backend_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
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
		Self:         fetchSelfInfo,
		RefusesEdges: true,
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
	}
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
