package stack

import (
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// browserSpace renders a browser-enabled space's compose services.
func browserSpace(t *testing.T, features ...string) (M, Settings) {
	t.Helper()
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1",
		Tools:   testTools(append(features, "telegram")...),
		Ingress: testIngress(features...)}
	dir := t.TempDir()
	return Compose(s, t.TempDir(), dir)["services"].(M), s
}

// SPEC-0082 R4.1: browser/meet spaces render a per-space sidecar owning the
// headed-Chromium display stack; the runtime keeps none of it.
func TestBrowserSidecarServiceRendered(t *testing.T) {
	services, s := browserSpace(t, "browser")
	sidecar, ok := services["browser-"+s.User].(M)
	if !ok {
		t.Fatalf("browser sidecar service missing: %#v", services)
	}
	if sidecar["image"] != "hermes-hub:0.4.0-prod-browser" {
		t.Fatalf("sidecar must run the browser role image, got %v", sidecar["image"])
	}
	if sidecar["build"].(M)["target"] != "prod-browser" {
		t.Fatalf("sidecar build must target prod-browser, got %v", sidecar["build"])
	}
	if sidecar["shm_size"] != "1gb" {
		t.Fatal("sidecar must keep the browser workload's shared memory budget")
	}
	// R4.1: the persistent profile and the workspace (downloads land where the
	// agent and user can reach them) are the only writable mounts.
	var profileMount, workspaceMount, novncMount M
	for _, raw := range sidecar["volumes"].([]any) {
		m := raw.(M)
		switch m["target"] {
		case "/state/browser":
			profileMount = m
		case "/workspace":
			workspaceMount = m
		case "/run/novnc.pass":
			novncMount = m
		case "/state":
			t.Fatal("sidecar must not mount the runtime state root")
		}
	}
	if profileMount == nil || !strings.HasSuffix(profileMount["source"].(string), "connections/browser") {
		t.Fatalf("sidecar missing per-space profile mount: %v", sidecar["volumes"])
	}
	if workspaceMount == nil {
		t.Fatal("sidecar must mount the workspace for downloads/artifacts")
	}
	// R4.2: noVNC authentication comes from a per-space secret file the
	// sidecar mounts read-only — x11vnc must never run -nopw again.
	if novncMount == nil || novncMount["read_only"] != true {
		t.Fatalf("sidecar must mount the noVNC password read-only: %v", sidecar["volumes"])
	}
	for _, raw := range sidecar["volumes"].([]any) {
		m := raw.(M)
		if src, _ := m["source"].(string); strings.Contains(src, "broker") || strings.Contains(src, "secrets") {
			t.Fatalf("sidecar must not see broker material: %v", m)
		}
	}
	// R4.2: noVNC publishes to host loopback only; CDP (9222) never publishes.
	ports, _ := sidecar["ports"].([]string)
	if len(ports) != 1 || ports[0] != "127.0.0.1:6080:6080" {
		t.Fatalf("sidecar must publish only loopback noVNC: %v", ports)
	}
	// The sidecar resolves as browser-<user> on the space-internal network.
	nets, _ := sidecar["networks"].(M)
	if _, ok := nets["default"].(M)["aliases"]; !ok {
		t.Fatalf("sidecar must alias browser-%s on the space network: %v", s.User, nets)
	}
	if !slices.Contains(nets["default"].(M)["aliases"].([]string), "browser-"+s.User) {
		t.Fatalf("sidecar alias missing: %v", nets)
	}
	// Meet spaces get the same sidecar — it hosts the upstream remote node.
	// The node shares the runtime's meetings dir (auth_state and out_dir RPC
	// paths resolve identically) and reads the token where upstream's
	// NodeServer looks: $HERMES_HOME/workspace/meetings/node_token.json.
	meetServices, ms := browserSpace(t, "meet")
	meetSidecar, ok := meetServices["browser-"+ms.User].(M)
	if !ok {
		t.Fatalf("meet space must render the browser sidecar: %#v", meetServices)
	}
	var tokenMount, meetingsMount M
	for _, raw := range meetSidecar["volumes"].([]any) {
		m := raw.(M)
		switch m["target"] {
		case "/state/hermes/workspace/meetings/node_token.json":
			tokenMount = m
		case "/state/hermes/workspace/meetings":
			meetingsMount = m
		}
	}
	if tokenMount == nil || tokenMount["read_only"] != true || !strings.HasSuffix(tokenMount["source"].(string), "meet-node-token.json") {
		t.Fatalf("meet sidecar must mount the rendered token at the NodeServer path, read-only: %v", meetSidecar["volumes"])
	}
	if meetingsMount == nil || meetingsMount["read_only"] == true || !strings.HasSuffix(meetingsMount["source"].(string), "hermes/workspace/meetings") {
		t.Fatalf("meet sidecar must share the meetings dir read-write: %v", meetSidecar["volumes"])
	}
	if meetSidecar["environment"].(M)["HUB_MEET"] != "true" {
		t.Fatal("meet sidecar must start the upstream NodeServer (HUB_MEET)")
	}
	if meetServices["hermes-runtime"].(M)["environment"].(M)["BROWSER_CDP_URL"] != "http://browser-"+ms.User+":9222" {
		t.Fatal("meet runtime must reach the sidecar CDP")
	}
}

// SPEC-0082 R4.3: runtime renders BROWSER_CDP_URL at the sidecar alias and
// carries no browser display surface of its own.
func TestRuntimeEnvCarriesCDPURL(t *testing.T) {
	services, s := browserSpace(t, "browser")
	runtime := services["hermes-runtime"].(M)
	env := runtime["environment"].(M)
	if env["BROWSER_CDP_URL"] != "http://browser-"+s.User+":9222" {
		t.Fatalf("runtime must reach the sidecar CDP by alias: %v", env["BROWSER_CDP_URL"])
	}
	for _, raw := range runtime["volumes"].([]any) {
		if raw.(M)["target"] == "/state/browser" {
			t.Fatal("runtime still mounts the browser profile — the sidecar owns it")
		}
	}
	for _, p := range runtime["ports"].([]string) {
		if strings.Contains(p, ":6080") {
			t.Fatalf("runtime still publishes noVNC: %v", p)
		}
	}
	if runtime["shm_size"] != nil {
		t.Fatal("runtime keeps shm for a browser workload it no longer runs")
	}
}

// SPEC-0082 R4.2/A4: no port in any rendered service exposes CDP to the host.
func TestCdpNotPublished(t *testing.T) {
	services, _ := browserSpace(t, "browser")
	for name, raw := range services {
		for _, p := range raw.(M)["ports"].([]string) {
			if strings.Contains(p, "9222") {
				t.Fatalf("%s publishes CDP: %v", name, p)
			}
		}
	}
}

// SPEC-0082 R4.3/R4.4: the browser MCP servers become URL servers on the
// sidecar — persistent profile via CDP, guest as an isolated instance.
func TestBrowserServersUseSidecarURL(t *testing.T) {
	s := Settings{User: "alice", Tools: testTools("browser"), Ingress: testIngress("browser")}
	servers := Config(s)["mcp_servers"].(M)
	persistent := servers["browser"].(M)
	guest := servers["browser_guest"].(M)
	if persistent["url"] != "http://browser-alice:8931/mcp" {
		t.Fatalf("persistent browser server must hit the sidecar MCP endpoint: %v", persistent["url"])
	}
	if guest["url"] != "http://browser-alice:8932/mcp" {
		t.Fatalf("guest browser server must hit the isolated sidecar instance: %v", guest["url"])
	}
	for _, server := range []M{persistent, guest} {
		if server["command"] != nil || server["args"] != nil {
			t.Fatalf("browser servers must not render as stdio in the runtime: %v", server)
		}
	}
}

// SPEC-0082 R4.2: render materializes the per-space noVNC password once, and
// meet seeds the node token the sidecar's upstream NodeServer serves.
func TestRenderMaterializesBrowserSecrets(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "alice"); err != nil {
		t.Fatal(err)
	}
	seedGovernance(t, d, func(doc *api.Governance) {
		activeToolGrant(t, doc, "alice", "native:meet", time.Hour)
	})
	settings, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	settings.Model = "test"
	settings.ModelURL = "http://model.invalid/v1"
	settings.Tools, settings.Ingress = testTools("workspace", "meet"), testIngress("workspace", "meet")
	if err := saveSpace(d, settings); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	passwd := filepath.Join(d, "browser-novnc.passwd")
	info, err := os.Stat(passwd)
	if err != nil {
		t.Fatalf("noVNC password file must exist: %v", err)
	}
	if goruntime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("noVNC password must be 0600, got %o", info.Mode().Perm())
	}
	body, _ := os.ReadFile(passwd)
	if len(strings.TrimSpace(string(body))) < 12 {
		t.Fatal("noVNC password too short")
	}
	// Meet: the node token feeds upstream's NodeServer token_path, and the
	// runtime-side registry seeds the approved sidecar entry so node='auto'
	// resolves without a manual approve step.
	tokenPath := filepath.Join(d, "meet-node-token.json")
	tokBody, err := os.ReadFile(tokenPath)
	if err != nil || !strings.Contains(string(tokBody), "\"token\"") {
		t.Fatalf("meet node token file missing: %v", err)
	}
	nodes := filepath.Join(d, "hermes", "workspace", "meetings", "nodes.json")
	nodesBody, err := os.ReadFile(nodes)
	if err != nil || !strings.Contains(string(nodesBody), "ws://browser-alice:18789") {
		t.Fatalf("node registry must seed the sidecar node: %v", err)
	}
	// Render twice: secrets must be stable, never rotated by re-render.
	passBefore, _ := os.ReadFile(passwd)
	if err := RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	passAfter, _ := os.ReadFile(passwd)
	if string(passBefore) != string(passAfter) {
		t.Fatal("re-render rotated the noVNC password")
	}
	// Browser-only spaces (no meet) still get the noVNC password — x11vnc
	// never runs unauthenticated — but no meet node material.
	db := t.TempDir()
	if err := Init(db, "bob"); err != nil {
		t.Fatal(err)
	}
	sb, err := Read(db)
	if err != nil {
		t.Fatal(err)
	}
	sb.Model = "test"
	sb.ModelURL = "http://model.invalid/v1"
	sb.Tools, sb.Ingress = testTools("workspace", "browser"), testIngress("workspace", "browser")
	if err := saveSpace(db, sb); err != nil {
		t.Fatal(err)
	}
	if err := RenderEnvironment(db, root, "prod"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(db, "browser-novnc.passwd")); err != nil {
		t.Fatalf("browser space missing noVNC password: %v", err)
	}
	if _, err := os.Stat(filepath.Join(db, "meet-node-token.json")); !os.IsNotExist(err) {
		t.Fatal("browser-only space must not get meet node material")
	}
}

// SPEC-0083 R1: the sidecar is the heaviest per-space workload — it carries
// an explicit cpu/memory/pids ceiling like toolhub, so one runaway Chromium
// cannot starve the host or its neighbours.
func TestBrowserSidecarResourceLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    Settings
	}{
		{"unmanaged", Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Model: "test", ModelURL: "http://model.invalid/v1", Tools: testTools("browser"), Ingress: testIngress("browser")}},
		{"managed", Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Model: "test", ModelURL: "http://model-relay:8318/v1", CapabilityMode: "managed", CapabilityProfileID: "managed-dev", CapabilityGeneration: 1, Tools: testTools("browser"), Ingress: testIngress("browser")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := Compose(tc.s, t.TempDir(), t.TempDir())["services"].(M)
			sidecar, ok := services["browser-"+tc.s.User].(M)
			if !ok {
				t.Fatalf("browser sidecar missing: %#v", services)
			}
			if sidecar["cpus"] != "2.0" {
				t.Fatalf("sidecar must cap cpu: %v", sidecar["cpus"])
			}
			if sidecar["mem_limit"] != "3g" {
				t.Fatalf("sidecar must cap memory (shm counts against it): %v", sidecar["mem_limit"])
			}
			if sidecar["pids_limit"] != 256 {
				t.Fatalf("sidecar must cap pids: %v", sidecar["pids_limit"])
			}
		})
	}
}

// Managed spaces reach the sidecar through the private agent network — the
// same per-space isolation the spawned runtime gets (SPEC-0082 R4.2).
func TestManagedBrowserSidecarOnAgentNetwork(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000,
		Model: "test", ModelURL: "http://model.invalid/v1", CapabilityMode: "managed",
		CapabilityProfileID: "managed-dev", CapabilityGeneration: 1,
		Tools: testTools("browser"), Ingress: testIngress("browser")}
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	sidecar, ok := services["browser-alice"].(M)
	if !ok {
		t.Fatalf("managed space must render the browser sidecar: %#v", services)
	}
	nets, _ := sidecar["networks"].(M)
	if _, ok := nets[managedAgentNetwork(s)]; !ok {
		t.Fatalf("managed sidecar must join the agent network: %v", nets)
	}
	if _, ok := nets["default"]; ok {
		t.Fatalf("managed sidecar must not sit on the project default network: %v", nets)
	}
}
