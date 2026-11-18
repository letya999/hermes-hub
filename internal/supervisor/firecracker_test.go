package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/stack"
)

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
