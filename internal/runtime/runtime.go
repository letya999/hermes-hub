package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
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
	"time"
)

type marker struct {
	PIDs    []int `json:"pids"`
	Browser bool  `json:"browser"`
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

func Run(args []string) error {
	if len(args) != 1 {
		return errors.New("expected idle, gateway, prepare, serve or health")
	}
	switch args[0] {
	case "health":
		return Health(filepath.Join(state, "runtime.json"))
	case "prepare":
		gid, err := strconv.Atoi(env("HUB_SHARED_GID", "1000"))
		if err != nil {
			return fmt.Errorf("HUB_SHARED_GID: %w", err)
		}
		return Prepare([]string{state, workspace}, 10001, gid, chown)
	case "idle", "gateway", "serve":
		if err := loadSelfEnv(); err != nil {
			return err
		}
		return supervise(args[0])
	default:
		return errors.New("expected idle, gateway, prepare, serve or health")
	}
}

func loadSelfEnv() error {
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

func Health(markerPath string) error {
	body, err := os.ReadFile(markerPath)
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
		response, err := client.Get(browserURL)
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
		if err := loadSelfEnv(); err != nil {
			return err
		}
	}
}

func superviseOnce(mode string) (bool, error) {
	setUmask()
	restartPath := filepath.Join(state, "restart.request")
	if err := os.Remove(restartPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	hermesHome := env("HERMES_HOME", filepath.Join(state, "hermes"))
	for _, folder := range []string{env("HOME", filepath.Join(state, "home")), hermesHome, filepath.Join(state, "browser"), filepath.Join(state, "cache"), filepath.Join(hermesHome, "skills"), filepath.Join(hermesHome, "hooks"), filepath.Join(hermesHome, "plugins"), filepath.Join(hermesHome, "memories")} {
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
		if _, statErr := os.Stat("/config/config.yaml"); statErr == nil {
			configErr = stack.MaterializeHermesConfig("/config/config.yaml", configDst, materializeOptionsFromEnv())
		} else {
			// No rendered source mount (legacy/test run): mutate in place.
			configErr = stack.ApplyHermesConfig(configDst, materializeOptionsFromEnv())
		}
		if configErr != nil {
			return false, configErr
		}
	}
	if err := copyIfExists("/config/SOUL.md", filepath.Join(hermesHome, "SOUL.md"), false); err != nil {
		return false, err
	}

	var children []*exec.Cmd
	exits := make(chan error, 8)
	remaining := 0
	var server *http.Server
	serverErr := make(chan error, 1)
	startWithEnv := func(extra map[string]string, name string, args ...string) error {
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
		children = append(children, cmd)
		remaining++
		go func() { exits <- cmd.Wait() }()
		return nil
	}
	start := func(name string, args ...string) error {
		return startWithEnv(nil, name, args...)
	}
	defer func() {
		if server != nil {
			shutdownRuntimeServer(server)
		}
		_ = os.Remove(filepath.Join(state, "runtime.json"))
		for i := len(children) - 1; i >= 0; i-- {
			stopProcess(children[i])
		}
		for range remaining {
			<-exits
		}
	}()

	browser := os.Getenv("HUB_BROWSER") == "true" || os.Getenv("HUB_MEET") == "true"
	if browser {
		if err := clearDisplayLocks(); err != nil {
			return false, err
		}
		if err := clearBrowserLocks(filepath.Join(state, "browser")); err != nil {
			return false, err
		}
		if err := os.Setenv("DISPLAY", ":99"); err != nil {
			return false, err
		}
		if err := start("Xvfb", ":99", "-screen", "0", "1440x900x24", "-nolisten", "tcp"); err != nil {
			return false, err
		}
		if err := waitForDisplay(":99"); err != nil {
			return false, err
		}
		for _, command := range [][]string{{"fluxbox"}, {"x11vnc", "-display", ":99", "-localhost", "-forever", "-shared", "-nopw"}, {"websockify", "--web=/usr/share/novnc", "0.0.0.0:6080", "127.0.0.1:5900"}, {"chromium", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage", "--password-store=basic", "--user-data-dir=/state/browser", "--remote-debugging-port=9222", "--remote-debugging-address=127.0.0.1", "about:blank"}} {
			if err := start(command[0], command[1:]...); err != nil {
				return false, err
			}
		}
		if err := waitForBrowser(); err != nil {
			return false, err
		}
	}
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
		server = &http.Server{Addr: env("HUB_RUNTIME_LISTEN", "0.0.0.0:8080"), Handler: runtimeHandler()}
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				serverErr <- err
			}
		}()
	}
	status := marker{PIDs: []int{os.Getpid()}, Browser: browser}
	for _, child := range children {
		status.PIDs = append(status.PIDs, child.Process.Pid)
	}
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
		case err := <-exits:
			remaining--
			return false, fmt.Errorf("supervised process exited: %w", err)
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
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(path); err != nil {
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
	".gz": true,
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
			_ = os.Remove(path)
			return nil
		}
		if info.Size() > browserOutputFileLimit || !browserOutputExts[strings.ToLower(filepath.Ext(path))] {
			_ = os.Remove(path)
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

func waitForBrowser() error {
	client := http.Client{Timeout: time.Second}
	for range 200 {
		response, err := client.Get(browserURL)
		if err == nil {
			if err := response.Body.Close(); err != nil {
				return err
			}
			return nil
		}
		sleep(100 * time.Millisecond)
	}
	return errors.New("chromium CDP did not start")
}

func waitForDisplay(display string) error {
	for range 100 {
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
	if f, err := os.OpenFile(destination, os.O_WRONLY, 0); err == nil {
		_ = f.Close()
		return true
	}
	if f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE, 0660); err == nil {
		_ = f.Close()
		return true
	}
	return false
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
	return os.WriteFile(destination, body, 0660)
}
