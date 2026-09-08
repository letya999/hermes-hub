package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
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
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func Run(args []string) error {
	if len(args) != 1 {
		return errors.New("expected idle, gateway, prepare or health")
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
		return supervise(args[0])
	default:
		return errors.New("expected idle, gateway, prepare or health")
	}
}

func Prepare(roots []string, uid, gid int, chown func(string, int, int) error) error {
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
	setUmask()
	for _, folder := range []string{"home", "hermes", "browser", "cache", "hermes/skills", "hermes/hooks", "hermes/plugins", "hermes/memories"} {
		if err := os.MkdirAll(filepath.Join(state, folder), 0770); err != nil {
			return err
		}
	}
	if err := copyIfExists("/config/config.yaml", filepath.Join(state, "hermes/config.yaml"), true); err != nil {
		return err
	}
	if err := copyIfExists("/config/SOUL.md", filepath.Join(state, "hermes/SOUL.md"), false); err != nil {
		return err
	}

	var children []*exec.Cmd
	exits := make(chan error, 8)
	start := func(name string, args ...string) error {
		cmd := command(name, args...)
		configureProcess(cmd)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		children = append(children, cmd)
		go func() { exits <- cmd.Wait() }()
		return nil
	}
	defer func() {
		_ = os.Remove(filepath.Join(state, "runtime.json"))
		for i := len(children) - 1; i >= 0; i-- {
			stopProcess(children[i])
		}
	}()

	browser := os.Getenv("HUB_BROWSER") == "true" || os.Getenv("HUB_MEET") == "true"
	if browser {
		if err := os.Setenv("DISPLAY", ":99"); err != nil {
			return err
		}
		for _, command := range [][]string{{"Xvfb", ":99", "-screen", "0", "1440x900x24", "-nolisten", "tcp"}, {"fluxbox"}, {"x11vnc", "-display", ":99", "-localhost", "-forever", "-shared", "-nopw"}, {"websockify", "--web=/usr/share/novnc", "0.0.0.0:6080", "127.0.0.1:5900"}, {"chromium", "--no-sandbox", "--no-first-run", "--disable-dev-shm-usage", "--password-store=basic", "--user-data-dir=/state/browser", "--remote-debugging-port=9222", "--remote-debugging-address=127.0.0.1", "about:blank"}} {
			if err := start(command[0], command[1:]...); err != nil {
				return err
			}
		}
		if err := waitForBrowser(); err != nil {
			return err
		}
	}
	if os.Getenv("HUB_MEET") == "true" {
		cmd := command("hermes", "plugins", "enable", "google_meet")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	if mode == "gateway" {
		if err := start("hermes", "gateway", "run"); err != nil {
			return err
		}
	}
	status := marker{PIDs: []int{os.Getpid()}, Browser: browser}
	for _, child := range children {
		status.PIDs = append(status.PIDs, child.Process.Pid)
	}
	body, _ := json.Marshal(status)
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), body, 0660); err != nil {
		return err
	}

	signals := make(chan os.Signal, 1)
	signalNotify(signals)
	defer signalStop(signals)
	for {
		select {
		case <-signals:
			return nil
		case err := <-exits:
			return fmt.Errorf("supervised process exited: %w", err)
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
