package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/diagnostics"
	"github.com/letya999/hermes-hub/internal/envstore"
	"github.com/letya999/hermes-hub/internal/stack"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type marker struct {
	PIDs    []int `json:"pids"`
	Browser bool  `json:"browser"`
}

// cdpProbeURL resolves the browser health endpoint: the sidecar's
// BROWSER_CDP_URL when the runtime reaches it over the internal network, the
// local default inside the sidecar itself (and under test).
func cdpProbeURL() string {
	if url := strings.TrimSuffix(os.Getenv("BROWSER_CDP_URL"), "/"); url != "" {
		return url + "/json/version"
	}
	return browserURL
}

var (
	state        = env("HUB_STATE", "/state")
	workspace    = env("HUB_WORKSPACE", "/workspace")
	sleep        = time.Sleep
	browserURL   = "http://127.0.0.1:9222/json/version"
	signalNotify = notifySignals
	signalStop   = stopSignals
	command      = exec.Command
	displayProbe = func(display string) error { return exec.Command("xdpyinfo", "-display", display).Run() }
	lookPath     = exec.LookPath
	chown        = chownPath
)

func toolHubAutostartBinary() string {
	if _, err := lookPath("hub-toolhub"); err == nil {
		return "hub-toolhub"
	}
	return "toolhub"
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// durationEnv reads a Go duration ("15m") or a bare seconds value; invalid
// input falls back so a typo cannot disable the watchdog.
func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func Run(args []string) error {
	if len(args) != 1 {
		return errors.New("expected idle, gateway, prepare, serve, health, model-relay, toolhub-relay, control-relay or egress-relay")
	}
	switch args[0] {
	case "model-relay":
		return runModelRelay(context.Background())
	case "toolhub-relay":
		return runToolHubRelay(context.Background())
	case "control-relay":
		return runControlRelay(context.Background())
	case "egress-relay":
		return runEgressRelay()
	case "verify-managed":
		if os.Getenv("HUB_CAPABILITY_MODE") != "managed" {
			return errors.New("verify-managed requires managed capability mode")
		}
		return managedPreflight()
	case "health":
		return Health(filepath.Join(state, "runtime.json"))
	case "prepare":
		gid, err := strconv.Atoi(env("HUB_SHARED_GID", "1000"))
		if err != nil {
			return fmt.Errorf("HUB_SHARED_GID: %w", err)
		}
		return Prepare([]string{state, workspace}, 10001, gid, chown)
	case "browser":
		return superviseBrowser()
	case "idle", "gateway", "serve":
		if os.Getenv("HUB_CAPABILITY_MODE") != "managed" {
			if err := loadSelfEnv(); err != nil {
				return err
			}
		}
		return supervise(args[0])
	default:
		return errors.New("expected idle, gateway, prepare, serve, health, browser, model-relay, toolhub-relay, control-relay or egress-relay")
	}
}

func loadSelfEnv() error {
	if os.Getenv("HUB_CAPABILITY_MODE") == "managed" {
		return errors.New("managed runtime forbids self-managed environment")
	}
	path := filepath.Join(state, envstore.FileName)
	for _, key := range []string{"ATLASSIAN_EMAIL", "ATLASSIAN_API_TOKEN", "ATLASSIAN_BASIC_AUTH"} {
		if err := os.Unsetenv(key); err != nil {
			return fmt.Errorf("clear legacy Atlassian env %s: %w", key, err)
		}
	}
	if _, err := envstore.Remove(path, os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"), "ATLASSIAN_EMAIL", "ATLASSIAN_API_TOKEN", "ATLASSIAN_BASIC_AUTH"); err != nil {
		return fmt.Errorf("remove legacy Atlassian env: %w", err)
	}
	values, err := envstore.Load(path, os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err != nil {
		return err
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set self-env %s: %w", key, err)
		}
	}
	return loadSelfServices()
}

func Prepare(roots []string, uid, gid int, chown func(string, int, int) error) error {
	privateFiles := map[string]bool{}
	for _, root := range roots {
		glabDir := filepath.Join(root, "home", ".config", "glab-cli")
		privateFiles[filepath.Join(glabDir, "config.yml")] = true
		privateFiles[filepath.Join(glabDir, "aliases.yml")] = true
		privateFiles[filepath.Join(root, "self-env.json")] = true
		privateFiles[filepath.Join(root, selfServicesFile)] = true
	}
	for _, root := range roots {
		if err := os.MkdirAll(root, 0770); err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return prepareEntry(path, entry, privateFiles[path], uid, gid, chown)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func prepareEntry(path string, entry fs.DirEntry, private bool, uid, gid int, chown func(string, int, int) error) error {
	if entry.Type()&os.ModeSymlink != 0 {
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	}
	// Unix sockets and other special files may be backed by a Docker Desktop
	// bind mount. They are runtime IPC artifacts, not state; chown/chmod on
	// them can block indefinitely on the host filesystem.
	if !entry.IsDir() && entry.Type()&os.ModeType != 0 {
		return nil
	}
	if err := chown(path, uid, gid); err != nil {
		return err
	}
	mode := fs.FileMode(0660)
	if entry.IsDir() {
		mode = 0770
	} else if private {
		mode = 0600
	}
	return os.Chmod(path, mode)
}

func runtimeDiagnosticsServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
}

func Health(markerPath string) error {
	body, err := os.ReadFile(markerPath) // #nosec G703 -- operator marker path
	if err != nil {
		return err
	}
	var status marker
	if err := json.Unmarshal(body, &status); err != nil || len(status.PIDs) == 0 {
		return errors.New("invalid runtime marker")
	}
	for _, pid := range status.PIDs {
		if !processAlive(pid) {
			return fmt.Errorf("process %d is not running", pid)
		}
	}
	if status.Browser {
		client := http.Client{Timeout: 2 * time.Second}
		response, err := client.Get(cdpProbeURL())
		if err != nil {
			return err
		}
		if err := response.Body.Close(); err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("browser health returned %s", response.Status)
		}
	}
	return nil
}

func supervise(mode string) error {
	for {
		restart, err := superviseOnce(mode)
		if err != nil || !restart {
			return err
		}
		if os.Getenv("HUB_CAPABILITY_MODE") != "managed" {
			if err := loadSelfEnv(); err != nil {
				return err
			}
		}
	}
}

// managedPreflight attests the mounted effective config and the container's
// actual isolation before any managed Hermes process may start.
func managedPreflight() error {
	hermesHome := env("HERMES_HOME", filepath.Join(state, "hermes"))
	if os.Getenv("HERMES_BUNDLES_DIR") != filepath.Join(hermesHome, "skill-bundles") ||
		os.Getenv("HERMES_ENABLE_PROJECT_PLUGINS") != "0" {
		return fmt.Errorf("managed runtime forbids redirected extension discovery")
	}
	opts, err := materializeOptionsFromEnv()
	if err != nil {
		return fmt.Errorf("managed runtime config preflight: %w", err)
	}
	if err := stack.ValidateManagedEffectiveConfig(filepath.Join(hermesHome, "config.yaml"),
		stack.Settings{Model: os.Getenv("HUB_MANAGED_MODEL_ID"), ModelURL: os.Getenv("HUB_MANAGED_MODEL_URL"), Timezone: os.Getenv("TZ")},
		opts); err != nil {
		return fmt.Errorf("managed runtime config preflight: %w", err)
	}
	if err := verifyManagedIsolation(hermesHome); err != nil {
		return fmt.Errorf("managed runtime isolation unverified: %w", err)
	}
	return nil
}

type childExit struct {
	name string
	err  error
}

// childSet owns the supervised-children lifecycle shared by the runtime and
// browser-sidecar supervisors: spawn, reap on exit, and kill-all teardown.
type childSet struct {
	mu        sync.Mutex
	children  []*exec.Cmd
	remaining int
	exits     chan childExit
}

func newChildSet() *childSet {
	return &childSet{exits: make(chan childExit, 8)}
}

func (c *childSet) startWithEnv(extra map[string]string, name string, args ...string) error {
	cmd := command(name, args...)
	configureProcess(cmd)
	if extra != nil {
		cmd.Env = append([]string{}, os.Environ()...)
		for key, value := range extra {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	c.mu.Lock()
	c.children = append(c.children, cmd)
	c.remaining++
	c.mu.Unlock()
	go func() { c.exits <- childExit{name, cmd.Wait()} }()
	return nil
}

func (c *childSet) start(name string, args ...string) error {
	return c.startWithEnv(nil, name, args...)
}

// exited marks one consumed exit report so stopAll's drain count stays exact.
func (c *childSet) exited() {
	c.mu.Lock()
	c.remaining--
	c.mu.Unlock()
}

// stopAll kills every child newest-first and drains their exit reports.
func (c *childSet) stopAll() {
	c.mu.Lock()
	stopped := append([]*exec.Cmd{}, c.children...)
	drain := c.remaining
	c.mu.Unlock()
	for i := len(stopped) - 1; i >= 0; i-- {
		stopProcess(stopped[i])
	}
	for range drain {
		<-c.exits
	}
}

func superviseOnce(mode string) (bool, error) {
	managed := os.Getenv("HUB_CAPABILITY_MODE") == "managed"
	if managed {
		if err := managedPreflight(); err != nil {
			return false, err
		}
	}
	setUmask()
	restartPath := filepath.Join(state, "restart.request")
	if err := os.Remove(restartPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	hermesHome := env("HERMES_HOME", filepath.Join(state, "hermes"))
	folders := []string{env("HOME", filepath.Join(state, "home")), hermesHome, filepath.Join(state, "cache"), filepath.Join(hermesHome, "memories")}
	if !managed {
		folders = []string{env("HOME", filepath.Join(state, "home")), hermesHome, filepath.Join(state, "cache"), filepath.Join(hermesHome, "skills"), filepath.Join(hermesHome, "hooks"), filepath.Join(hermesHome, "plugins"), filepath.Join(hermesHome, "memories")}
	}
	for _, folder := range folders {
		if err := os.MkdirAll(folder, 0770); err != nil {
			return false, err
		}
	}
	// The effective Hermes config is materialized on the host and mounted
	// read-only over the agent-writable state dir: mcp_servers cannot be
	// mutated from inside the runtime. When it is missing (legacy mount) the
	// runtime materializes the config itself as before.
	configDst := filepath.Join(hermesHome, "config.yaml")
	if effectiveConfigWritable(configDst) {
		var configErr error
		var opts stack.MaterializeOptions
		if opts, configErr = materializeOptionsFromEnv(); configErr == nil {
			if _, statErr := os.Stat("/config/config.yaml"); statErr == nil {
				configErr = stack.MaterializeHermesConfig("/config/config.yaml", configDst, opts)
			} else {
				// No rendered source mount (legacy/test run): mutate in place.
				configErr = stack.ApplyHermesConfig(configDst, opts)
			}
		}
		if configErr != nil {
			return false, configErr
		}
	}
	if err := copySoulUnlessEdited("/config/SOUL.md", filepath.Join(hermesHome, "SOUL.md")); err != nil {
		return false, err
	}

	children := newChildSet()
	var server *http.Server
	serverErr := make(chan error, 1)
	start := children.start
	startWithEnv := children.startWithEnv
	defer func() {
		if server != nil {
			shutdownRuntimeServer(server)
		}
		_ = os.Remove(filepath.Join(state, "runtime.json"))
		children.stopAll()
	}()

	// SPEC-0082 R4: the browser display stack lives in the per-space sidecar.
	// The flag survives only to drive the output sweep and the remote-CDP
	// health probe — this process never spawns X/VNC/Chromium again.
	browser := os.Getenv("HUB_BROWSER") == "true" || os.Getenv("HUB_MEET") == "true"
	if os.Getenv("HUB_MEET") == "true" {
		cmd := command("hermes", "plugins", "enable", "google_meet")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return false, err
		}
	}
	if mode == "gateway" {
		if err := start("hub-communication"); err != nil {
			return false, err
		}
	}
	if mode == "serve" {
		if os.Getenv("HUB_RUNTIME_AUTH") == "" {
			return false, errors.New("HUB_RUNTIME_AUTH is required")
		}
		if os.Getenv("HUB_TOOLHUB_AUTOSTART") == "true" {
			if os.Getenv("HUB_TOOLHUB_STORE") == "" {
				return false, errors.New("HUB_TOOLHUB_STORE is required when ToolHub autostart is enabled")
			}
			if err := start(toolHubAutostartBinary()); err != nil {
				return false, err
			}
		}
		if err := startWithEnv(hermesGatewayEnvironment(), "hermes", "gateway", "run", "--no-supervise", "--force"); err != nil {
			return false, err
		}
		server = runtimeDiagnosticsServer(env("HUB_RUNTIME_LISTEN", "0.0.0.0:8080"), diagnostics.HTTP("runtime", runtimeHandler()))
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		}()
	}
	status := marker{PIDs: []int{os.Getpid()}, Browser: browser}
	children.mu.Lock()
	for _, child := range children.children {
		status.PIDs = append(status.PIDs, child.Process.Pid)
	}
	children.mu.Unlock()
	body, _ := json.Marshal(status)
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), body, 0660); err != nil {
		return false, err
	}

	signals := make(chan os.Signal, 1)
	signalNotify(signals)
	defer signalStop(signals)
	var sweep <-chan time.Time
	if browser {
		// Playwright has no per-file/type policy; the sweep is the bounded
		// enforcement for downloads and screenshots under /workspace/browser.
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		sweep = ticker.C
	}
	for {
		select {
		case <-sweep:
			sweepBrowserOutput(filepath.Join(workspace, "browser"))
		case err := <-serverErr:
			return false, err
		case <-signals:
			if server != nil {
				shutdownRuntimeServer(server)
			}
			if err := os.Remove(restartPath); err == nil {
				return true, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			return false, nil
		case exit := <-children.exits:
			children.exited()
			if exit.err == nil {
				// A clean exit still loses the incident if it is reported as
				// %!w(<nil>): say which child chose to stop.
				return false, fmt.Errorf("supervised process %s exited cleanly", exit.name)
			}
			return false, fmt.Errorf("supervised process %s exited: %w", exit.name, exit.err)
		}
	}
}

// superviseBrowser runs the per-space browser sidecar (SPEC-0082 R4, ADR-0083
// D3): the headed-Chromium display stack that used to live inside the runtime.
// Chromium binds CDP on all container interfaces — reachability is governed by
// the space-internal network, never by a published port. x11vnc authenticates
// against the mounted password file and noVNC's HTTP/WS bridge is the only
// host-published surface (loopback, by Compose).
func superviseBrowser() error {
	setUmask()
	if err := clearDisplayLocks(); err != nil {
		return err
	}
	profile := filepath.Join(state, "browser")
	if err := os.MkdirAll(profile, 0770); err != nil {
		return err
	}
	if err := clearBrowserLocks(profile); err != nil {
		return err
	}
	if err := os.Setenv("DISPLAY", ":99"); err != nil {
		return err
	}
	children := newChildSet()
	defer children.stopAll()
	start := children.start
	if err := start("Xvfb", ":99", "-screen", "0", "1440x900x24", "-nolisten", "tcp"); err != nil {
		return err
	}
	if err := waitForDisplay(":99", nil); err != nil {
		return err
	}
	for _, proc := range [][]string{
		{"fluxbox"},
		{"x11vnc", "-display", ":99", "-localhost", "-forever", "-shared", "-passwdfile", "/run/novnc.pass"},
		{"websockify", "--web=/opt/novnc", "0.0.0.0:6080", "127.0.0.1:5900"},
		{"chromium", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage", "--password-store=basic", "--user-data-dir=" + profile, "--remote-debugging-port=9222", "--remote-debugging-address=0.0.0.0", "about:blank"},
	} {
		if err := start(proc[0], proc[1:]...); err != nil {
			return err
		}
	}
	if err := waitForBrowser(nil); err != nil {
		return err
	}
	// Both playwright-mcp endpoints answer streamable HTTP on the internal
	// network. Persistent (8931) attaches the signed-in Chromium over CDP —
	// same owner profile as the legacy in-runtime stack. Guest (8932) is a
	// second instance with --isolated on the system chromium: in-memory
	// profile, no /state/browser access, no shared CDP session.
	mcp := "/opt/browser/node_modules/@playwright/mcp/cli.js"
	for _, args := range [][]string{
		{mcp, "--host", "0.0.0.0", "--port", "8931", "--caps", "vision,pdf", "--output-dir", "/workspace/browser", "--output-max-size", "268435456", "--block-service-workers", "--cdp-endpoint", "http://127.0.0.1:9222"},
		{mcp, "--host", "0.0.0.0", "--port", "8932", "--caps", "vision,pdf", "--output-dir", "/workspace/browser", "--output-max-size", "268435456", "--block-service-workers", "--isolated", "--executable-path", "/usr/bin/chromium", "--no-sandbox"},
	} {
		if err := start("node", args...); err != nil {
			return err
		}
	}
	if os.Getenv("HUB_MEET") == "true" {
		// ADR-0083 D3: the gateway's google_meet plugin drives this node over
		// the token-authenticated WebSocket (registry seeded at render). The
		// node mounts the shared meetings dir at the same path the runtime
		// sees, so auth_state and out_dir payloads resolve identically.
		err := children.startWithEnv(map[string]string{
			"PYTHONPATH": "/opt/hermes",
			// The node's home is the container path the meetings dir mounts at
			// — identical to the runtime's view, so auth_state/out_dir resolve.
			"HERMES_HOME": "/state/hermes",
		}, "/opt/meet/.venv/bin/python", "-c",
			"import asyncio; from plugins.google_meet.node.server import NodeServer; "+
				"s = NodeServer(host='0.0.0.0', port=18789); s.ensure_token(); asyncio.run(s.serve())")
		if err != nil {
			return err
		}
	}
	signals := make(chan os.Signal, 1)
	signalNotify(signals)
	defer signalStop(signals)
	for {
		select {
		case <-signals:
			return nil
		case exit := <-children.exits:
			children.exited()
			if exit.err == nil {
				return fmt.Errorf("sidecar process %s exited cleanly", exit.name)
			}
			return fmt.Errorf("sidecar process %s exited: %w", exit.name, exit.err)
		}
	}
}

func hermesGatewayEnvironment() map[string]string {
	return map[string]string{
		"API_SERVER_ENABLED":    "true",
		"API_SERVER_KEY":        os.Getenv("HUB_RUNTIME_AUTH"),
		"API_SERVER_HOST":       env("HUB_HERMES_API_HOST", "127.0.0.1"),
		"API_SERVER_PORT":       env("HUB_HERMES_API_PORT", "8642"),
		"HERMES_GATEWAY_NO_TTY": "true",
		// The Hub supplies the human response through the native run approval API.
		"HERMES_EXEC_ASK":              "true",
		"HERMES_DISABLE_LAZY_INSTALLS": "1",
	}
}

func clearDisplayLocks(paths ...string) error {
	if len(paths) == 0 {
		paths = []string{"/tmp/.X99-lock", "/tmp/.X11-unix/X99"}
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func clearBrowserLocks(dir string) error {
	for _, name := range []string{"SingletonCookie", "SingletonLock", "SingletonSocket"} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path) // #nosec G703 -- browser profile dir from runtime layout
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(path); err != nil { // #nosec G703 -- browser profile dir from runtime layout
				return err
			}
		}
	}
	return nil
}

const (
	browserOutputTotalLimit = 256 << 20
	browserOutputFileLimit  = 64 << 20
	browserOutputMaxEntries = 4096
)

// browserOutputExts allows documents, images, media and in-flight download
// partials; executables and scripts never persist in the workspace.
var browserOutputExts = map[string]bool{
	"": true, ".txt": true, ".md": true, ".csv": true, ".tsv": true, ".json": true,
	".xml": true, ".yml": true, ".yaml": true, ".html": true, ".htm": true,
	".har": true, ".mhtml": true, ".pdf": true, ".png": true, ".jpg": true,
	".jpeg": true, ".gif": true, ".webp": true, ".svg": true, ".zip": true,
	".gz":  true,
	".tar": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true, ".odt": true, ".ods": true, ".odp": true,
	".mp3": true, ".wav": true, ".ogg": true, ".mp4": true, ".webm": true,
	".mov": true, ".crdownload": true, ".part": true, ".download": true, ".tmp": true,
}

type browserOutputFile struct {
	path    string
	size    int64
	modTime time.Time
}

// sweepBrowserOutput enforces the download policy: symlinks, oversized files
// and disallowed types are removed, then oldest files are evicted until the
// directory is under the total cap. Enforcement is post-write eviction, not a
// pre-write gate; a burst can briefly exceed the cap between sweeps.
func sweepBrowserOutput(dir string) {
	entries := 0
	var kept []browserOutputFile
	var total int64
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entries++; entries > browserOutputMaxEntries {
			return fs.SkipAll
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || d.Type()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
			_ = os.Remove(path) // #nosec G122 -- walked path inside the browser output dir
			return nil
		}
		if info.Size() > browserOutputFileLimit || !browserOutputExts[strings.ToLower(filepath.Ext(path))] {
			_ = os.Remove(path) // #nosec G122 -- walked path inside the browser output dir
			return nil
		}
		total += info.Size()
		kept = append(kept, browserOutputFile{path: path, size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if total <= browserOutputTotalLimit {
		return
	}
	slices.SortFunc(kept, func(a, b browserOutputFile) int { return a.modTime.Compare(b.modTime) })
	for _, file := range kept {
		if total <= browserOutputTotalLimit {
			return
		}
		if err := os.Remove(file.path); err == nil {
			total -= file.size
		}
	}
}

var errStopped = errors.New("stopped")

func waitForBrowser(stop <-chan struct{}) error {
	client := http.Client{Timeout: time.Second}
	for range 600 {
		select {
		case <-stop:
			return errStopped
		default:
		}
		response, err := client.Get(browserURL)
		if err == nil {
			if err := response.Body.Close(); err != nil {
				return err
			}
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		sleep(100 * time.Millisecond)
	}
	return errors.New("chromium CDP did not start")
}

func waitForDisplay(display string, stop <-chan struct{}) error {
	for range 100 {
		select {
		case <-stop:
			return errStopped
		default:
		}
		if displayProbe(display) == nil {
			return nil
		}
		sleep(100 * time.Millisecond)
	}
	return errors.New("xvfb display did not start")
}

// effectiveConfigWritable reports whether the effective Hermes config path is
// mutable inside the container. A read-only bind mount over the file is the
// enforced signal that the host already materialized the config; a missing
// file on a writable directory means the runtime must materialize it itself.
func effectiveConfigWritable(destination string) bool {
	if f, err := os.OpenFile(destination, os.O_WRONLY, 0); err == nil { // #nosec G703 -- effective config destination from operator layout
		_ = f.Close()
		return true
	}
	if f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE, 0660); err == nil { // #nosec G703 -- effective config destination from operator layout
		_ = f.Close()
		return true
	}
	return false
}

// copySoulUnlessEdited replaces a missing SOUL or the Init stub. Any other
// bytes are the user's own instructions. A read-only mount of the host file
// is already the source of truth, so a failed overwrite there is ignored.
func copySoulUnlessEdited(source, destination string) error {
	existing, readErr := os.ReadFile(destination)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if readErr == nil && !stack.IsUserSoulStub(existing) {
		return nil
	}
	body, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if readErr == nil && string(existing) == string(body) {
		return nil
	}
	if err := os.WriteFile(destination, body, 0660); err != nil { // #nosec G703 -- effective config destination from operator layout
		if readOnlyWrite(err) {
			return nil
		}
		return err
	}
	return nil
}

func readOnlyWrite(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "read-only") || strings.Contains(text, "read only") || strings.Contains(text, "erofs")
}

func copyIfExists(source, destination string, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(destination); err == nil {
			return nil
		}
	}
	body, err := os.ReadFile(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(destination, body, 0660) // #nosec G703 -- effective config destination from operator layout
}
