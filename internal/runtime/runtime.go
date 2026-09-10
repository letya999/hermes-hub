package runtime

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/envstore"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
		return Prepare([]string{state, workspace}, 10001, gid, chownPath)
	case "idle", "gateway":
		if err := loadSelfEnv(); err != nil {
			return err
		}
		return supervise(args[0])
	case "serve":
		return Serve()
	default:
		return errors.New("expected idle, gateway, prepare, serve or health")
	}
}

func Serve() error {
	if err := loadSelfEnv(); err != nil {
		return err
	}
	userID := env("HUB_RUNTIME_USER_ID", "me")
	orgID := os.Getenv("HUB_RUNTIME_ORGANIZATION_ID")
	if orgID == "" || orgID == "personal" {
		orgID = "personal"
	}
	if os.Getenv("HUB_RUNTIME_TOKEN") == "" {
		return errors.New("HUB_RUNTIME_TOKEN is required")
	}
	bind := Binding{UserID: userID, OrganizationID: orgID, UserHome: env("HUB_RUNTIME_USER_HOME", "/scope/user"), OrganizationHome: os.Getenv("HUB_RUNTIME_ORGANIZATION_HOME"), Features: strings.Split(os.Getenv("HUB_FEATURES"), ","), Env: currentEnv(), OrgActions: parseList(os.Getenv("HUB_ORG_ACTIONS"))}
	server := NewServer(bind, os.Getenv("HUB_RUNTIME_TOKEN"), nil)
	bindState := filepath.Join(bind.UserHome, "connections")
	if err := os.MkdirAll(bindState, 0700); err != nil {
		return err
	}
	markerPath := filepath.Join(bindState, "runtime.json")
	body, _ := json.Marshal(marker{PIDs: []int{os.Getpid()}})
	if err := os.WriteFile(markerPath, body, 0600); err != nil {
		return err
	}
	defer os.Remove(markerPath)
	listener, err := net.Listen("tcp", env("HUB_RUNTIME_BIND", ":9090"))
	if err != nil {
		return err
	}
	defer listener.Close()
	return http.Serve(listener, server.Handler())
}

func parseList(raw string) map[string]bool {
	result := map[string]bool{}
	for _, item := range strings.Split(raw, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result[item] = true
		}
	}
	return result
}

func currentEnv() map[string]string {
	result := map[string]string{}
	for _, item := range os.Environ() {
		if key, value, ok := strings.Cut(item, "="); ok {
			result[key] = value
		}
	}
	return result
}

func loadSelfEnv() error {
	values, err := envstore.Load(filepath.Join(state, envstore.FileName), os.Getenv("HUB_SELF_ENV_KEYS"), os.Getenv("HUB_PROTECTED_ENV_KEYS"))
	if err != nil {
		return err
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set self-env %s: %w", key, err)
		}
	}
	if email, token := os.Getenv("ATLASSIAN_EMAIL"), os.Getenv("ATLASSIAN_API_TOKEN"); email != "" && token != "" {
		if err := os.Setenv("ATLASSIAN_BASIC_AUTH", base64.StdEncoding.EncodeToString([]byte(email+":"+token))); err != nil {
			return fmt.Errorf("set Atlassian auth: %w", err)
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
	}
	for _, root := range roots {
		if err := os.MkdirAll(root, 0770); err != nil {
			return err
		}
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if err := chown(path, uid, gid); err != nil {
				return err
			}
			mode := fs.FileMode(0660)
			if entry.IsDir() {
				mode = 0770
			} else if privateFiles[path] {
				mode = 0600
			}
			return os.Chmod(path, mode)
		})
		if err != nil {
			return err
		}
	}
	return nil
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
	for _, folder := range []string{"home", "hermes", "browser", "cache", "hermes/skills", "hermes/hooks", "hermes/plugins", "hermes/memories"} {
		if err := os.MkdirAll(filepath.Join(state, folder), 0770); err != nil {
			return false, err
		}
	}
	if err := copyIfExists("/config/config.yaml", filepath.Join(state, "hermes/config.yaml"), true); err != nil {
		return false, err
	}
	if err := applySelfServices(filepath.Join(state, "hermes/config.yaml")); err != nil {
		return false, err
	}
	if err := copyIfExists("/config/SOUL.md", filepath.Join(state, "hermes/SOUL.md"), false); err != nil {
		return false, err
	}

	var children []*exec.Cmd
	exits := make(chan error, 8)
	remaining := 0
	start := func(name string, args ...string) error {
		cmd := command(name, args...)
		configureProcess(cmd)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		children = append(children, cmd)
		remaining++
		go func() { exits <- cmd.Wait() }()
		return nil
	}
	defer func() {
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
		if err := start("communication-hub"); err != nil {
			return false, err
		}
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
		case <-signals:
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
