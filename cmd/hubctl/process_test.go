package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/shared/locale"
	"github.com/letya999/hermes-hub/shared/testkit"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

// fixtureLayers mirrors the normative managed-space layer set the spawn
// tests bootstrap with — the message path can only run once a space's
// revisions have been activated into a stored effective digest.
func fixtureLayers() map[string]map[string]map[string]any {
	return map[string]map[string]map[string]any{
		"platform-ceiling": {
			"stack": {
				"identity": map[string]any{"user": "alice", "organization": "local"},
				"services": map[string]any{
					"hermes-runtime":    map[string]any{"mode": "supervised"},
					"browser":           map[string]any{"mode": "per-space"},
					"toolhub":           map[string]any{"mode": "shared"},
					"model-relay":       map[string]any{"mode": "shared"},
					"communication-hub": map[string]any{"mode": "shared"},
					"credential-broker": map[string]any{"mode": "shared"},
					"supervisor":        map[string]any{"mode": "shared"},
					"cliproxy":          map[string]any{"mode": "per-space"},
					"hub-stt":           map[string]any{"mode": "shared"},
					"hub-tts":           map[string]any{"mode": "shared"},
				},
			},
			"config": {
				"memory": map[string]any{"backend": []any{"local", "honcho"}},
			},
			"tools": {"core": map[string]any{
				"native:file": map[string]any{"via": "native"},
				"files":       map[string]any{"via": "toolhub"},
				"terminal":    map[string]any{"via": "toolhub"},
			}},
			"workspace": {
				"writable":  []any{"/workspace", "/vault"},
				"read_only": []any{"/config", "/skills", "/shared"},
			},
			"models": {
				"services": map[string]any{
					"cliproxy":   map[string]any{"via": "cliproxy"},
					"direct-key": map[string]any{"via": "openai-compatible"},
				},
				"routes": map[string]any{
					"everyday": map[string]any{"ladder": []any{"cliproxy"}},
				},
			},
			"grants": {"allow": []any{"workspace", "tools", "config"}},
		},
		"platform-template": {
			"config": {
				"memory":  map[string]any{"backend": "local"},
				"runtime": map[string]any{"timezone": "UTC"},
			},
			"tools":     {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"workspace": {"writable": []any{"/workspace"}, "read_only": []any{"/config", "/skills"}},
			"models": {
				"default": "everyday",
				"services": map[string]any{
					"cliproxy": map[string]any{
						"via":      "cliproxy",
						"endpoint": "http://cliproxy:8317/v1",
						"auth": map[string]any{
							"upstream":     map[string]any{"kind": "oauth-session", "ref": "cliproxy-main"},
							"proxy_access": map[string]any{"kind": "broker-key", "ref": "cliproxy"},
						},
					},
				},
			},
		},
		"org-ceiling": {
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": []any{"workspace", "tools"}},
		},
		"org-template": {
			"config":    {"memory": map[string]any{"backend": "honcho"}},
			"workspace": {"writable": []any{"/workspace"}},
		},
		"user": {
			"config": {"memory": map[string]any{"backend": "local"}, "runtime": map[string]any{"timezone": "Europe/Berlin"}},
			"tools":  {"core": map[string]any{"native:file": map[string]any{"via": "native"}, "files": map[string]any{"via": "toolhub"}}},
			"grants": {"allow": map[string]any{
				"workspace": []any{"read", "write"},
				"tools":     []any{"list", "use"},
			}},
		},
	}
}

// bootstrapSpace drives the real v1 path: create the space, submit every
// layer, validate them, activate the set — the same calls a control-plane
// client makes before its first message may claim a runtime.
func bootstrapSpace(t *testing.T, ctx context.Context, client spawnv1.Client, tenant spawnv1.Tenant, space string) {
	t.Helper()
	if _, err := client.CreateSpace(ctx, spawnv1.CreateSpaceRequest{
		Org: tenant.Org, Owner: tenant.Owner, ID: space, Environment: "dev", IdempotencyKey: "create-" + space,
	}); err != nil {
		t.Fatalf("create space: %v", err)
	}
	layers := fixtureLayers()
	ids := map[string]string{}
	for _, layer := range spawnv1.Layers {
		docs, ok := layers[layer]
		if !ok {
			continue
		}
		rev, err := client.SubmitRevision(ctx, space, layer, spawnv1.SubmitRevisionRequest{
			Org: tenant.Org, Owner: tenant.Owner, Documents: docs, IdempotencyKey: "submit-" + space + "-" + layer,
		})
		if err != nil {
			t.Fatalf("submit %s: %v", layer, err)
		}
		ids[layer] = rev.ID
	}
	var all []string
	for _, layer := range spawnv1.Layers {
		id, ok := ids[layer]
		if !ok {
			continue
		}
		rev, err := client.ValidateRevision(ctx, space, id, spawnv1.RevisionRequest{
			Org: tenant.Org, Owner: tenant.Owner, IdempotencyKey: "validate-" + space + "-" + layer,
		})
		if err != nil {
			t.Fatalf("validate %s %s: %v", layer, id, err)
		}
		if rev.State != "validated" {
			t.Fatalf("validate %s %s: state %s detail %s", layer, id, rev.State, rev.Detail)
		}
		all = append(all, id)
	}
	if _, err := client.ActivateRevisions(ctx, space, spawnv1.ActivationRequest{
		Org: tenant.Org, Owner: tenant.Owner, Revisions: all, IdempotencyKey: "activate-" + space,
	}); err != nil {
		t.Fatalf("activate %v: %v", all, err)
	}
}

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
			"datadir": os.Getenv("HUB_DATA_DIR"),
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
			services := []ServiceDescriptor{{Name: "fixture-one", DataDir: "fixture-one"}, {Name: "fixture-two", DataDir: "fixture-two", Edges: []string{"fixture-one"}}}
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
				path := filepath.Join(data, svc.Name) + ".db.started"
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
				if report["declared"] != "allowed" || report["undeclared"] != "" || report["database"] != "sqlite://"+filepath.ToSlash(filepath.Join(data, svc.Name+".db")) || report["datadir"] != filepath.Join(data, svc.DataDir) {
					t.Fatalf("incorrect launch environment for %s", svc.Name)
				}
				if info, err := os.Stat(filepath.Join(data, svc.DataDir)); err != nil || !info.IsDir() {
					t.Fatalf("service data dir missing for %s: %v", svc.Name, err)
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
	unt := locale.T(locale.Operator(), "hubctl.run-untrusted")
	for _, tc := range []struct {
		name    string
		envFile string
		envVar  string
		want    string
	}{
		{name: "default-no-declaration", want: unt},
		{name: "env-file-false", envFile: "HUB_TRUSTED=false\n", want: unt},
		{name: "env-file-overrides-process-env", envFile: "HUB_TRUSTED=false\n", envVar: "true", want: unt},
		{name: "corrupt-env-file", envFile: "lowercase=bad\n", want: locale.T(locale.Operator(), "hubctl.run-env")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("HUB_TRUSTED", tc.envVar)
			if tc.envFile != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.envFile), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			data := filepath.Join(t.TempDir(), "absent")
			err := runProcesses(context.Background(), []string{"--mode", "process", "--profile", "minimal", "--data", data})
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("want %q refusal, got %v", tc.want, err)
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatalf("refused launch touched data: %v", err)
			}
		})
	}
}

func TestProcessModeMissingCoreDoesNotCreateData(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("HUB_TRUSTED=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "absent")
	err := runProcesses(t.Context(), []string{"--bin", t.TempDir(), "--data", data})
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
		if names[svc.Name] || ports[svc.Port] || svc.Binary == "" || svc.DataDir != svc.Name || filepath.Ext(svc.DataDir) != "" || len(svc.Env) == 0 || svc.Port < 1024 {
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
	for _, svc := range services {
		if svc.Spawned && svc.Zone != "space" {
			t.Fatalf("non-space service marked spawned: %+v", svc)
		}
	}
	minimal := profileServices("minimal")
	if len(minimal) != 2 || minimal[0].Name != "spawn-controller" || minimal[1].Name != "harness-runtime" {
		t.Fatalf("minimal: %+v", minimal)
	}
	if len(profileServices("family")) != 9 || len(profileServices("enterprise")) != 13 {
		t.Fatal("profile membership drift")
	}
	if launched := launchedServices("minimal"); len(launched) != 1 || launched[0].Name != "spawn-controller" {
		t.Fatalf("minimal launch set: %+v", launched)
	}
	if len(launchedServices("family")) != 8 || len(launchedServices("enterprise")) != 12 {
		t.Fatal("launch set drift")
	}
}

func TestProcessEnvironmentContainsOnlyDeclaredValues(t *testing.T) {
	t.Setenv("UNDECLARED_PROVIDER_TOKEN", "synthetic-private")
	t.Setenv("DECLARED_TEST_VALUE", "allowed")
	svc := profileServices("minimal")[1]
	svc.Env = []string{"DECLARED_TEST_VALUE"}
	env := strings.Join(processEnv(svc, profileServices("minimal"), "data", "tok"), "\n")
	if strings.Contains(env, "synthetic-private") || !strings.Contains(env, "DECLARED_TEST_VALUE=allowed") || !strings.Contains(env, "HUB_SPAWN_CONTROLLER_URL=http://127.0.0.1:9201") || strings.Contains(env, "HUB_TOOL_HUB_URL=") || !strings.Contains(env, "harness-runtime.db") || !strings.Contains(env, "HUB_SERVICE_TOKEN=tok") || !strings.Contains(env, "HUB_DATA_DIR="+filepath.Join("data", "harness-runtime")) {
		t.Fatalf("environment wiring: %s", env)
	}
}

// The minimal profile answers a real message end to end: the launcher
// starts spawn-controller on a free port, the controller spawns a
// harness-runtime child for the (space, agent) pair, and the child answers
// through the fake model server. No fixture stands in for the binaries,
// the launcher or the message path — and the service surface refuses
// unauthenticated, browser-origin and non-JSON calls.
func TestMinimalProfileAnswersMessage(t *testing.T) {
	binDir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	for _, svc := range []string{"spawn-controller", "harness-runtime"} {
		build := exec.Command("go", "build", "-o", filepath.Join(binDir, svc+suffix), "./services/"+svc)
		build.Dir = "../.."
		if body, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", svc, err, body)
		}
	}
	model := testkit.ModelServer(t, "pong-", "answer")
	t.Setenv("HUB_MANAGED_MODEL_URL", model.URL+"/v1")
	t.Setenv("HUB_MANAGED_MODEL_ID", "fixture-model")
	t.Setenv("HUB_SERVICE_TOKEN", "test-service-token")
	// The controller compiles revisions against the pinned catalog — point
	// its env at the repository tree before the working directory moves.
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_CATALOG_ROOT", repoRoot)
	services := launchedServices("minimal")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	services[0].Port = listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	base := "http://127.0.0.1:" + strconv.Itoa(services[0].Port)
	dir := t.TempDir()
	t.Chdir(dir)
	data := filepath.Join(dir, "data")
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- launchProcesses(ctx, services, binDir, data)
	}()
	// The launcher's trust gate is covered by its own test; here the process
	// surface itself must be real. Poll until it answers or the launcher dies.
	deadline := time.Now().Add(60 * time.Second)
	healthy := false
	for !healthy {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			healthy = resp.StatusCode == http.StatusOK
			_ = resp.Body.Close()
		}
		if !healthy {
			if time.Now().After(deadline) {
				t.Fatal("spawn-controller never came up")
			}
			select {
			case err := <-done:
				t.Fatalf("launcher exited: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	// The space the message claims must exist with an activated effective
	// digest — bootstrap it through the same contract a control-plane
	// client uses; a message for an unmanaged space is refused, never
	// spawned.
	tenant := spawnv1.Tenant{Org: "local", Owner: "alice"}
	bootstrapSpace(t, ctx, spawnv1.NewHTTPClient(base, "test-service-token", nil), tenant, "s1")
	post := func(body, token string, headers map[string]string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, base+"/v1/messages?org=local&owner=alice", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}
	var reply string
	for reply == "" {
		status, body := post(`{"space":"s1","agent":"a1","text":"ping"}`, "test-service-token", map[string]string{"Content-Type": "application/json"})
		if status == http.StatusOK {
			var out struct {
				Reply string `json:"reply"`
			}
			if json.Unmarshal(body, &out) == nil {
				reply = out.Reply
			}
		}
		if reply == "" {
			if time.Now().After(deadline) {
				t.Fatal("minimal profile did not answer within 60s")
			}
			select {
			case err := <-done:
				t.Fatalf("launcher exited: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	if reply != "pong-answer" {
		t.Fatalf("reply %q", reply)
	}
	// The same surface must refuse what a stray caller or browser could send.
	for _, tc := range []struct {
		name    string
		token   string
		headers map[string]string
		want    int
	}{
		{"no token", "", map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
		{"wrong token", "nope", map[string]string{"Content-Type": "application/json"}, http.StatusUnauthorized},
		{"browser origin", "test-service-token", map[string]string{"Content-Type": "application/json", "Origin": "http://evil.example"}, http.StatusForbidden},
		{"form post", "test-service-token", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
	} {
		t.Run("refuse-"+tc.name, func(t *testing.T) {
			if status, _ := post(`{"space":"s1","agent":"a1","text":"ping"}`, tc.token, tc.headers); status != tc.want {
				t.Fatalf("status %d, want %d", status, tc.want)
			}
		})
	}
	req, _ := http.NewRequest(http.MethodGet, base+"/v1/runtimes?org=local&owner=alice", nil)
	req.Header.Set("Authorization", "Bearer test-service-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// The runtime inventory is the durable row: claimed for the message,
	// released back to warm when the hold ended.
	if !strings.Contains(string(body), `"space_id":"s1"`) || !strings.Contains(string(body), `"state":"warm"`) {
		t.Fatalf("spawned runtime missing: %s", body)
	}
	// The written token file is how local tooling authenticates.
	if raw, err := os.ReadFile(filepath.Join(data, "service.token")); err != nil || strings.TrimSpace(string(raw)) != "test-service-token" {
		t.Fatalf("service.token: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("launcher: %v", err)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(services[0].Port), time.Second); err == nil {
		_ = conn.Close()
		t.Fatal("spawn-controller survived launcher shutdown")
	}
}
