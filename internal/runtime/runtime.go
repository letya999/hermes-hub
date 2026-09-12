package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/envstore"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	chown        = chownPath
)

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
	if err := copyIfExists("/config/config.yaml", filepath.Join(hermesHome, "config.yaml"), true); err != nil {
		return false, err
	}
	if err := applySelfServices(filepath.Join(hermesHome, "config.yaml")); err != nil {
		return false, err
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
		for _, command := range [][]string{{"Xvfb", ":99", "-screen", "0", "1440x900x24", "-nolisten", "tcp"}, {"fluxbox"}, {"x11vnc", "-display", ":99", "-localhost", "-forever", "-shared", "-nopw"}, {"websockify", "--web=/usr/share/novnc", "0.0.0.0:6080", "127.0.0.1:5900"}, {"chromium", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage", "--password-store=basic", "--user-data-dir=/state/browser", "--remote-debugging-port=9222", "--remote-debugging-address=127.0.0.1", "about:blank"}} {
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
	for {
		select {
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
