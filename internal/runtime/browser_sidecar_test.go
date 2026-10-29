package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type spawnedCmd struct {
	cmd  *exec.Cmd
	argv string
}

// captureCommands replaces the command factory: every spawned child is a
// blocking helper process while its argv is recorded at factory time — the
// record survives children whose Env is replaced by startWithEnv.
func captureCommands(t *testing.T) (spawned *[]spawnedCmd, restore func()) {
	t.Helper()
	// Children spawned with a replaced Env inherit the test process env, so
	// the helper flag rides through os.Environ().
	t.Setenv("HUB_TEST_HELPER", "1")
	oldState, oldCommand := state, command
	oldNotify, oldStop, oldProbe := signalNotify, signalStop, displayProbe
	state = t.TempDir()
	var mu sync.Mutex
	var list []spawnedCmd
	command = func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperProcess")
		cmd.Env = append(os.Environ(), "HUB_TEST_HELPER=1")
		mu.Lock()
		list = append(list, spawnedCmd{cmd: cmd, argv: name + " " + strings.Join(args, " ")})
		mu.Unlock()
		return cmd
	}
	displayProbe = func(string) error { return nil }
	signalNotify = func(ch chan os.Signal) { go func() { time.Sleep(150 * time.Millisecond); ch <- os.Interrupt }() }
	signalStop = func(chan os.Signal) {}
	return &list, func() {
		mu.Lock()
		defer mu.Unlock()
		state, command = oldState, oldCommand
		signalNotify, signalStop, displayProbe = oldNotify, oldStop, oldProbe
	}
}

// SPEC-0082 R4.3: the runtime image carries no browser display stack — with
// HUB_BROWSER set the supervisor must spawn none of the X/VNC/Chromium
// processes the sidecar now owns.
func TestRuntimeNeverSpawnsBrowserStack(t *testing.T) {
	spawned, restore := captureCommands(t)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldURL := browserURL
	browserURL = server.URL
	t.Cleanup(func() { browserURL = oldURL })
	t.Setenv("HUB_BROWSER", "true")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("idle"); err != nil {
		t.Fatal(err)
	}
	for _, child := range *spawned {
		for _, banned := range []string{"Xvfb", "fluxbox", "x11vnc", "websockify", "chromium"} {
			if strings.Contains(child.argv, banned) {
				t.Fatalf("runtime spawned %s: %s", banned, child.argv)
			}
		}
	}
}

// SPEC-0082 R4.1/R4.2/R4.4: `hub-runtime browser` is the sidecar supervisor —
// the headed Chromium display stack, the authenticated VNC bridge and both
// playwright-mcp endpoints, all inside the browser image.
func TestBrowserSidecarCommandTree(t *testing.T) {
	spawned, restore := captureCommands(t)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldURL := browserURL
	browserURL = server.URL
	t.Cleanup(func() { browserURL = oldURL })
	t.Setenv("HUB_MEET", "false")
	t.Setenv("HUB_BROWSER_HOST", "browser-alice")
	if err := Run([]string{"browser"}); err != nil {
		t.Fatal(err)
	}
	var argvs []string
	for _, child := range *spawned {
		argvs = append(argvs, child.argv)
	}
	joined := "\n" + strings.Join(argvs, "\n")
	for _, want := range []string{
		"Xvfb :99",
		"fluxbox",
		"x11vnc",
		"websockify",
		"chromium",
		"--remote-debugging-port=9223",
		"--user-data-dir=" + filepath.Join(state, "browser"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sidecar tree missing %q:\n%s", want, joined)
		}
	}
	// R4.2: Chromium keeps DevTools on loopback — Chrome ≥126 ignores
	// --remote-debugging-address, so hub-runtime's TCP relay serves the
	// contract :9222 on the space network; the host never sees it.
	if strings.Contains(joined, "--remote-debugging-address") {
		t.Fatalf("dead flag must not come back — the relay owns :9222:\n%s", joined)
	}
	// R4.2: noVNC requires authentication — x11vnc reads the mounted password
	// file; -nopw must never appear again.
	x11 := slices.IndexFunc(argvs, func(a string) bool { return strings.HasPrefix(a, "x11vnc") })
	if x11 < 0 || !strings.Contains(argvs[x11], "-passwdfile /run/novnc.pass") {
		t.Fatalf("x11vnc must authenticate via the mounted password file:\n%s", joined)
	}
	if strings.Contains(joined, "-nopw") {
		t.Fatalf("unauthenticated VNC is forbidden:\n%s", joined)
	}
	// R4.3/R4.4: persistent pw-mcp attaches the owner profile over CDP on
	// 8931; the guest launches an isolated system Chromium on 8932 that can
	// never reach /state/browser.
	persistent := slices.IndexFunc(argvs, func(a string) bool { return strings.Contains(a, "--port 8931") })
	if persistent < 0 || !strings.Contains(argvs[persistent], "--cdp-endpoint http://127.0.0.1:9223") {
		t.Fatalf("persistent playwright-mcp must attach CDP:\n%s", joined)
	}
	// The MCP SDK compares req.headers.host verbatim against --allowed-hosts;
	// without the space alias every call via browser-<user>:<port> 403s.
	if !strings.Contains(argvs[persistent], "browser-alice:8931") {
		t.Fatalf("persistent endpoint must allow its alias:port Host header:\n%s", argvs[persistent])
	}
	guest := slices.IndexFunc(argvs, func(a string) bool { return strings.Contains(a, "--port 8932") })
	if guest < 0 || !strings.Contains(argvs[guest], "--isolated") || !strings.Contains(argvs[guest], "--executable-path") {
		t.Fatalf("guest playwright-mcp must launch isolated system chromium:\n%s", joined)
	}
	if !strings.Contains(argvs[guest], "browser-alice:8932") {
		t.Fatalf("guest endpoint must allow its alias:port Host header:\n%s", argvs[guest])
	}
	if strings.Contains(argvs[guest], filepath.Join(state, "browser")) || strings.Contains(argvs[guest], "--cdp-endpoint") {
		t.Fatalf("guest must not reach the owner profile or its CDP session:\n%s", argvs[guest])
	}
	// The sidecar mounts only /state/browser — /state itself is read-only, so
	// the image's HOME=/state/home is unusable. Chromium, Fluxbox and
	// fontconfig all need a writable home; it must live inside the profile
	// mount.
	home := os.Getenv("HOME")
	profile := filepath.Join(state, "browser")
	if !strings.HasPrefix(home, profile+string(filepath.Separator)) {
		t.Fatalf("HOME must resolve inside the writable profile mount, got %q", home)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("HOME dir must exist: %v", err)
	}
}

// SPEC-0082 R4 + ADR-0083 D3: Meet-enabled spaces run the upstream remote
// NodeServer inside the sidecar; the runtime reaches it over the rendered
// ws:// alias with the shared token.
func TestBrowserSidecarMeetNode(t *testing.T) {
	spawned, restore := captureCommands(t)
	defer restore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldURL := browserURL
	browserURL = server.URL
	t.Cleanup(func() { browserURL = oldURL })
	t.Setenv("HUB_MEET", "true")
	if err := Run([]string{"browser"}); err != nil {
		t.Fatal(err)
	}
	var node *exec.Cmd
	for _, child := range *spawned {
		if strings.Contains(child.argv, "NodeServer") {
			node = child.cmd
		}
	}
	if node == nil {
		t.Fatal("meet sidecar must start the upstream NodeServer")
	}
	env := strings.Join(node.Env, "\n")
	if !strings.Contains(env, "PYTHONPATH=/opt/hermes") || !strings.Contains(env, "HERMES_HOME=/state/hermes") {
		t.Fatalf("meet node needs the upstream plugin tree and its own HERMES_HOME:\n%s", env)
	}
}

// SPEC-0082 R4.2 regression: Chrome ≥126 pins DevTools to loopback and 500s
// non-IP Host headers, so the space network reaches CDP through hub-runtime's
// reverse proxy — it must forward requests and rewrite Host to loopback.
func TestCDPRelayForwardsAndRewritesHost(t *testing.T) {
	var seenHost atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHost.Store(r.Host)
		fmt.Fprint(w, "devtools-ok")
	}))
	defer upstream.Close()
	target := strings.TrimPrefix(upstream.URL, "http://")
	relay, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	go serveCDPRelay(relay, target)
	resp, err := http.Get("http://" + relay.Addr().String() + "/json/version")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "devtools-ok" {
		t.Fatalf("relay did not forward the request: %q", body)
	}
	if seenHost.Load() != target {
		t.Fatalf("upstream must see a loopback Host header, got %v", seenHost.Load())
	}
}

// SPEC-0082 R4.3: the runtime's browser health probe follows the sidecar —
// marker.Browser checks the remote CDP URL, not a local process.
func TestRuntimeHealthProbesRemoteCDP(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	t.Setenv("BROWSER_CDP_URL", server.URL)
	path := filepath.Join(t.TempDir(), "runtime.json")
	body, _ := json.Marshal(marker{PIDs: []int{os.Getpid()}, Browser: true})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(path); err != nil {
		t.Fatal(err)
	}
	if hits == 0 {
		t.Fatal("health probe did not reach the remote CDP endpoint")
	}
}
