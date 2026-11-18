package supervisor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/uno"
)

// fakeUno serves the Uno-compatible control-plane API the backend client
// calls: box list/launch/delete plus run submission.
type fakeUno struct {
	boxes    []uno.Box
	launched chan uno.LaunchRequest
	runs     chan uno.RunRequest
}

func startFakeUno(t *testing.T, boxes []uno.Box) (*httptest.Server, *fakeUno) {
	t.Helper()
	f := &fakeUno{boxes: boxes, launched: make(chan uno.LaunchRequest, 4), runs: make(chan uno.RunRequest, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/boxes":
			_ = json.NewEncoder(w).Encode(map[string]any{"boxes": f.boxes})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/boxes":
			var req uno.LaunchRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.launched <- req
			_ = json.NewEncoder(w).Encode(map[string]any{"box": uno.Box{
				ID: "box-1", Name: req.Name, Image: req.Image, Status: "running",
				VsockUDS: filepath.Join(t.TempDir(), "v.sock"), Labels: req.Labels,
			}})
		case r.Method == http.MethodDelete && len(parts) == 3 && parts[1] == "boxes":
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
		case r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "boxes" && parts[3] == "runs":
			var req uno.RunRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.runs <- req
			status := "succeeded"
			if strings.Contains(req.Cmd, "boot-fail") {
				status = "failed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"run": uno.Run{ID: "run-1", Status: status, IdempotencyKey: req.IdempotencyKey}})
		default:
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func fcBackend(t *testing.T, m *Manager, unoURL string) *firecrackerBackend {
	t.Helper()
	m.cfg.UnoURL, m.cfg.UnoToken, m.cfg.UnoImage = unoURL, "tok", "fc-img"
	m.cfg.UnoProxyHost = "127.0.0.1"
	b, err := newFirecrackerBackend(m)
	if err != nil {
		t.Fatal(err)
	}
	return b.(*firecrackerBackend)
}

func TestFCRelayProxiesParsesAddHostPairs(t *testing.T) {
	args := []string{
		"--add-host", "model-relay:172.24.0.2",
		"--add-host", "toolhub:172.24.0.3",
		"--add-host", "egress-relay:172.24.0.4",
		"--add-host", "unrelated:172.24.0.5",
	}
	proxies, ports := fcRelayProxies(args, "127.0.0.1")
	if len(proxies) != 3 {
		t.Fatalf("proxies: %v", proxies)
	}
	seen := map[int]string{}
	for _, p := range proxies {
		seen[p.GuestPort] = p.Target
	}
	for port, want := range map[int]string{8318: "127.0.0.1:8318", 8090: "127.0.0.1:8090", 8319: "127.0.0.1:8319"} {
		if seen[port] != want {
			t.Fatalf("port %d target %q, want %q", port, seen[port], want)
		}
	}
	if strings.Join(ports, ",") != "8318,8090,8319" {
		t.Fatalf("ports: %v", ports)
	}
}

func TestFCRelayProxiesRoutableHost(t *testing.T) {
	proxies, _ := fcRelayProxies([]string{"--add-host", "model-relay:10.0.0.2"}, "192.168.1.10")
	if len(proxies) != 1 || proxies[0].Target != "192.168.1.10:8318" {
		t.Fatalf("proxies: %v", proxies)
	}
}

func TestFCGuestMountsRewriteUnderPersist(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()
	binding := Binding{ContextRoot: dir}
	settings := stack.Settings{User: "alice"}
	policy := filepath.Join(dir, "generated", "policy.json")
	config := filepath.Join(dir, "generated", "config.yaml")
	mounts, err := m.fcGuestMounts(binding, settings, "dev", policy, config)
	if err != nil {
		t.Fatal(err)
	}
	var cfg fcMount
	found := false
	for _, mt := range mounts {
		if !strings.HasPrefix(mt.src, "/persist") {
			t.Fatalf("mount %s src %q not under /persist", mt.dst, mt.src)
		}
		if mt.dst == "/state/hermes/config.yaml" {
			cfg, found = mt, true
		}
	}
	if !found || !cfg.ro {
		t.Fatalf("config mount missing or not read-only: %v", mounts)
	}
}

func TestFCGuestMountsRejectsEscape(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()
	outside := t.TempDir()
	binding := Binding{ContextRoot: dir, OrganizationRoot: outside}
	_, err := m.fcGuestMounts(binding, stack.Settings{User: "alice"}, "dev",
		filepath.Join(dir, "p.json"), filepath.Join(dir, "c.yaml"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected escape rejection, got %v", err)
	}
}

func TestFCGuestMountsRejectsShellUnsafe(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()
	binding := Binding{ContextRoot: dir, OrganizationRoot: filepath.Join(dir, "o'org")}
	_, err := m.fcGuestMounts(binding, stack.Settings{User: "alice"}, "dev",
		filepath.Join(dir, "p.json"), filepath.Join(dir, "c.yaml"))
	if err == nil || !strings.Contains(err.Error(), "shell-safe") {
		t.Fatalf("expected shell-safe rejection, got %v", err)
	}
}

func TestFCGuestEnvRewritesSpacePaths(t *testing.T) {
	m := &Manager{}
	dir := t.TempDir()
	managed := filepath.Join(dir, "managed-runtime.dev.env")
	if err := os.WriteFile(managed, []byte("HUB_EXTRA="+dir+"/managed/dev/state\nOTHER=plain\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(dir, "runtime.env")
	if err := os.WriteFile(auth, []byte("FROM_FILE=yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binding := Binding{
		ContextRoot:   dir,
		EnvFile:       auth,
		runtimeAuth:   "tok",
		UserID:        "alice",
		PrincipalID:   "alice",
		ContextID:     "ctx",
		RuntimeID:     "rt",
		PolicyVersion: "policy-1",
	}
	env, err := fcGuestEnv(m, binding, Runtime{Generation: "gen-1"}, 8080, stack.Settings{User: "alice"}, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if env["HUB_EXTRA"] != "/persist/managed/dev/state" {
		t.Fatalf("HUB_EXTRA: %q", env["HUB_EXTRA"])
	}
	for key, want := range map[string]string{
		"HUB_RUNTIME_LISTEN":     "127.0.0.1:8080",
		"HUB_RUNTIME_AUTH":       "tok",
		"HERMES_HOME":            "/state/hermes",
		"HUB_RUNTIME_GENERATION": "gen-1",
		"FROM_FILE":              "yes",
	} {
		if env[key] != want {
			t.Fatalf("%s=%q, want %q", key, env[key], want)
		}
	}
	if !strings.Contains(env["PATH"], "/opt/hermes/.venv/bin") {
		t.Fatalf("PATH: %q", env["PATH"])
	}
}

func TestFirecrackerBackendRequiresUnoConfig(t *testing.T) {
	if _, err := newFirecrackerBackend(&Manager{cfg: Config{}}); err == nil {
		t.Fatal("expected error without Uno config")
	}
	if _, err := newFirecrackerBackend(&Manager{cfg: Config{UnoURL: "http://u", UnoToken: "t"}}); err == nil {
		t.Fatal("expected error without Uno image")
	}
	if _, err := newFirecrackerBackend(&Manager{cfg: Config{UnoURL: "http://u", UnoToken: "t", UnoImage: "img"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFirecrackerBackendManagedOnly(t *testing.T) {
	b, err := newFirecrackerBackend(&Manager{cfg: Config{UnoURL: "http://u", UnoToken: "t", UnoImage: "img"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Spawn(t.Context(), SpawnRequest{Runtime: Runtime{Managed: false}}); err == nil {
		t.Fatal("expected managed-only rejection")
	}
}

func TestFCGuestBootScriptMountContract(t *testing.T) {
	script := fcGuestBootScript([]fcMount{
		{dst: "/state/hermes/config.yaml", src: "/persist/generated/config.yaml", ro: true},
		{dst: "/workspace", src: "/persist/managed/dev/workspace"},
	})
	for _, want := range []string{
		"mount -t ext4 /dev/vdb /persist",
		"mount -o bind,ro '/persist/generated/config.yaml' '/state/hermes/config.yaml'",
		"mount -o bind '/persist/managed/dev/workspace' '/workspace'",
		"model-relay toolhub toolhub-relay egress-relay",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
}

// fcRelayCommand stubs the docker CLI surface sandboxRelayHosts and the
// control-relay spawn need: relay discovery on the agent network plus the
// generic "ok" for run/network verbs.
func fcRelayCommand(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 3 && args[0] == "ps" {
		return []byte("c1 c2 c3\n"), nil
	}
	if len(args) > 3 && args[0] == "inspect" && strings.HasPrefix(args[2], "{{json (index .NetworkSettings.Networks") {
		aliases := map[string][]string{"c1": {"toolhub"}, "c2": {"model-relay"}, "c3": {"egress-relay"}}
		ips := map[string]string{"c1": "10.90.0.2", "c2": "10.90.0.3", "c3": "10.90.0.4"}
		out, _ := json.Marshal(map[string]any{"IPAddress": ips[args[3]], "Aliases": aliases[args[3]]})
		return out, nil
	}
	return []byte("ok"), nil
}

func TestFCEnsureManagedNets(t *testing.T) {
	m, _ := testManager(t, fcRelayCommand, nil)
	net, err := m.ensureManagedNetsFC(t.Context(), stack.Settings{User: "alice"})
	if err != nil || net != "hermes-hub-agent-alice-prod" {
		t.Fatalf("ensureManagedNetsFC: %q %v", net, err)
	}
}

func TestFCEnsureManagedNetsRejectsPublicAgentNet(t *testing.T) {
	m, _ := testManager(t, func(ctx context.Context, args ...string) ([]byte, error) {
		if len(args) > 3 && args[0] == "network" && args[1] == "inspect" && args[3] == "{{.Internal}}" {
			return []byte("false"), nil
		}
		return []byte("ok"), nil
	}, nil)
	if _, err := m.ensureManagedNetsFC(t.Context(), stack.Settings{User: "alice"}); err == nil || !strings.Contains(err.Error(), "not internal") {
		t.Fatalf("public agent net accepted: %v", err)
	}
}

func TestFCFindBoxOwnedVerifyObserveRemove(t *testing.T) {
	stale := uno.Box{ID: "box-old", Name: "cont-1", Status: "failed", CreatedAt: time.Now().Add(-time.Hour)}
	fresh := uno.Box{ID: "box-new", Name: "cont-1", Status: "running", Address: "127.0.0.1:41000", Labels: map[string]string{"hermes.context": "ctx-1"}, CreatedAt: time.Now()}
	srv, _ := startFakeUno(t, []uno.Box{stale, fresh})
	m, _ := testManager(t, fcRelayCommand, nil)
	b := fcBackend(t, m, srv.URL)

	box, err := b.findBox(t.Context(), "cont-1")
	if err != nil || box.ID != "box-new" {
		t.Fatalf("findBox: %+v %v", box, err)
	}
	if _, err := b.findBox(t.Context(), "ghost"); err != uno.ErrNotFound {
		t.Fatalf("findBox missing: %v", err)
	}

	rt := Runtime{Container: "cont-1", ContextID: "ctx-1"}
	if err := b.Verify(t.Context(), rt); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := b.Verify(t.Context(), Runtime{Container: "cont-1", ContextID: "other"}); err == nil {
		t.Fatal("context mismatch accepted")
	}
	obs, err := b.Observe(t.Context(), rt)
	if err != nil || obs.ID != "box-new" || obs.State != "running" {
		t.Fatalf("Observe: %+v %v", obs, err)
	}
	if obs.Address != "http://127.0.0.1:41000" {
		t.Fatalf("Observe address not rewritten: %q", obs.Address)
	}
	// A pre-set http address survives untouched.
	obs, err = b.Observe(t.Context(), Runtime{Container: "cont-1", ContextID: "ctx-1", Address: "http://relay:8091"})
	if err != nil || obs.Address != "http://relay:8091" {
		t.Fatalf("Observe existing address: %+v %v", obs, err)
	}
	// Remove deletes the box and tolerates the absent control-relay sidecar.
	rt.Managed = true
	rt.Generation = "gen-1"
	if err := b.Remove(t.Context(), rt); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Missing box still reaps the sidecar attempt without failing.
	if err := b.Remove(t.Context(), Runtime{Container: "ghost", Managed: true, Generation: "gen-1"}); err != nil {
		t.Fatalf("Remove missing box: %v", err)
	}
}

func TestFCFindBoxFallsBackToStale(t *testing.T) {
	old := uno.Box{ID: "box-old", Name: "cont-1", Status: "failed", CreatedAt: time.Now().Add(-time.Hour)}
	older := uno.Box{ID: "box-older", Name: "cont-1", Status: "failed", CreatedAt: time.Now().Add(-2 * time.Hour)}
	srv, _ := startFakeUno(t, []uno.Box{old, older})
	m, _ := testManager(t, fcRelayCommand, nil)
	b := fcBackend(t, m, srv.URL)
	box, err := b.findBox(t.Context(), "cont-1")
	if err != nil || box.ID != "box-old" {
		t.Fatalf("stale fallback should pick newest failed box: %+v %v", box, err)
	}
}

func TestFCSpawnEndToEnd(t *testing.T) {
	srv, fake := startFakeUno(t, nil)
	m, root := testManager(t, fcRelayCommand, nil)
	writeManagedFixtures(t, root)
	b := fcBackend(t, m, srv.URL)

	bound, err := m.normalize(binding(root))
	if err != nil {
		t.Fatal(err)
	}
	settings, err := stack.ReadEffective(root, "prod")
	if err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Managed: true, Container: "fc-alice-1", Generation: "gen-1", ContextID: bound.ContextID, Address: "http://127.0.0.1:40000"}
	obs, err := b.Spawn(t.Context(), SpawnRequest{Binding: bound, Runtime: rt, Settings: settings, Port: 40000})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if obs.ID != "box-1" || obs.Address != rt.Address {
		t.Fatalf("observation: %+v", obs)
	}
	req := <-fake.launched
	if req.Name != "fc-alice-1" || req.Image != "fc-img" || len(req.Shares) != 1 || req.Shares[0].HostPath != bound.ContextRoot {
		t.Fatalf("launch request: %+v", req)
	}
	if req.Labels["hermes.context"] != bound.ContextID || req.Labels["hermes.generation"] != "gen-1" {
		t.Fatalf("launch labels: %v", req.Labels)
	}
	if len(req.Proxies) == 0 || req.Env["HUB_FC_INGRESS"] == "" {
		t.Fatalf("relay proxies missing: %+v", req)
	}
	// Boot + serve runs were submitted with generation keys.
	keys := map[string]bool{}
	for i := 0; i < 2; i++ {
		keys[(<-fake.runs).IdempotencyKey] = true
	}
	if !keys["boot-gen-1"] || !keys["serve-gen-1"] {
		t.Fatalf("run keys: %v", keys)
	}
}
