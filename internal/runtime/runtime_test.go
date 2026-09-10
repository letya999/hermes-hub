package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrepareDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(state, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(state, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	seen := map[string]bool{}
	if err := Prepare([]string{state}, 1, 2, func(path string, _, _ int) error { seen[path] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen[outside] || seen[filepath.Join(outside, "secret")] || seen[filepath.Join(state, "link")] {
		t.Fatal("Prepare followed or modified a symlink")
	}
}

func TestHealthAndRunValidation(t *testing.T) {
	if processAlive(-1) {
		t.Fatal("invalid pid accepted")
	}
	path := filepath.Join(t.TempDir(), "runtime.json")
	body, _ := json.Marshal(marker{PIDs: []int{os.Getpid()}})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(path); err != nil {
		t.Fatal(err)
	}
	oldState := state
	state = filepath.Dir(path)
	t.Cleanup(func() { state = oldState })
	if err := Run([]string{"health"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if Health(path) == nil || Run(nil) == nil || Run([]string{"bad"}) == nil {
		t.Fatal("invalid state accepted")
	}
	body, _ = json.Marshal(marker{PIDs: []int{999999999}})
	if err := os.WriteFile(path, body, 0600); err != nil || Health(path) == nil {
		t.Fatal("dead process accepted")
	}
}

func TestRunRejectsInvalidGID(t *testing.T) {
	t.Setenv("HUB_SHARED_GID", "bad")
	if err := Run([]string{"prepare"}); err == nil {
		t.Fatal("invalid gid accepted")
	}
}

func TestLoadSelfEnv(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Setenv("GITHUB_TOKEN", "original")
	t.Setenv("HUB_SELF_ENV_KEYS", "GITHUB_TOKEN")
	t.Setenv("HUB_PROTECTED_ENV_KEYS", "ORG_TOKEN")
	t.Cleanup(func() { state = oldState })
	if err := os.WriteFile(filepath.Join(state, "self-env.json"), []byte(`{"GITHUB_TOKEN":"updated"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GITHUB_TOKEN") != "updated" {
		t.Fatal("self env was not loaded")
	}
	if err := os.WriteFile(filepath.Join(state, "self-env.json"), []byte(`{"ORG_TOKEN":"blocked"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfEnv(); err == nil {
		t.Fatal("protected self env was loaded")
	}
}

func TestBrowserHealthAndWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldURL := browserURL
	browserURL = server.URL
	t.Cleanup(func() { browserURL = oldURL })
	path := filepath.Join(t.TempDir(), "runtime.json")
	body, _ := json.Marshal(marker{PIDs: []int{os.Getpid()}, Browser: true})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(path); err != nil {
		t.Fatal(err)
	}
	if err := waitForBrowser(); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserWaitFailure(t *testing.T) {
	oldURL, oldSleep := browserURL, sleep
	browserURL, sleep = "http://127.0.0.1:1", func(time.Duration) {}
	t.Cleanup(func() { browserURL, sleep = oldURL, oldSleep })
	if err := waitForBrowser(); err == nil {
		t.Fatal("unavailable browser accepted")
	}
}

func TestSupervisorCleansUpAfterStartFailure(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	t.Cleanup(func() { state = oldState })
	if err := supervise("gateway"); err == nil {
		t.Fatal("missing Hermes executable accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "runtime.json")); !os.IsNotExist(err) {
		t.Fatal("runtime marker left behind")
	}
}

func TestSupervisorBrowserAndMeetStartFailures(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	for _, tc := range []struct{ browser, meet string }{{"true", "false"}, {"false", "true"}} {
		t.Setenv("HUB_BROWSER", tc.browser)
		t.Setenv("HUB_MEET", tc.meet)
		if err := supervise("idle"); err == nil {
			t.Fatal("missing runtime executable accepted")
		}
	}
}

func TestSupervisorReportsChildExit(t *testing.T) {
	oldState, oldCommand := state, command
	state = t.TempDir()
	command = func(string, ...string) *exec.Cmd { return exec.Command("cmd.exe", "/c", "exit", "3") }
	t.Cleanup(func() { state, command = oldState, oldCommand })
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("gateway"); err == nil {
		t.Fatal("child exit accepted")
	}
}

func TestSupervisorBrowserShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldState, oldCommand, oldURL := state, command, browserURL
	oldNotify, oldStop := signalNotify, signalStop
	state, browserURL = t.TempDir(), server.URL
	command = func(string, ...string) *exec.Cmd {
		return exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Sleep -Seconds 10")
	}
	signalNotify = func(ch chan os.Signal) { ch <- os.Interrupt }
	signalStop = func(chan os.Signal) {}
	t.Cleanup(func() {
		state, command, browserURL = oldState, oldCommand, oldURL
		signalNotify, signalStop = oldNotify, oldStop
	})
	t.Setenv("HUB_BROWSER", "true")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("idle"); err != nil {
		t.Fatal(err)
	}
}

func TestRunPrepareAndIdleShutdown(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	oldNotify, oldStop := signalNotify, signalStop
	state, workspace = filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "workspace")
	signalNotify = func(ch chan os.Signal) { ch <- os.Interrupt }
	signalStop = func(chan os.Signal) {}
	t.Cleanup(func() { state, workspace, signalNotify, signalStop = oldState, oldWorkspace, oldNotify, oldStop })
	t.Setenv("HUB_SHARED_GID", "1000")
	if err := Run([]string{"prepare"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervise("idle") }()
	markerPath := filepath.Join(state, "runtime.json")
	for range 100 {
		if _, err := os.Stat(markerPath); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not stop")
	}
}

func TestSupervisorRestartsInPlaceAfterEnvUpdate(t *testing.T) {
	oldState, oldNotify, oldStop := state, signalNotify, signalStop
	state = t.TempDir()
	calls := 0
	signalNotify = func(ch chan os.Signal) {
		calls++
		if calls == 1 {
			if err := os.WriteFile(filepath.Join(state, "restart.request"), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		ch <- os.Interrupt
	}
	signalStop = func(chan os.Signal) {}
	t.Cleanup(func() { state, signalNotify, signalStop = oldState, oldNotify, oldStop })
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("idle"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected in-place restart, got %d supervisor runs", calls)
	}
}

func TestRunIdleLoadsEnvAndStops(t *testing.T) {
	oldState, oldNotify, oldStop := state, signalNotify, signalStop
	state = t.TempDir()
	signalNotify = func(ch chan os.Signal) { ch <- os.Interrupt }
	signalStop = func(chan os.Signal) {}
	t.Cleanup(func() { state, signalNotify, signalStop = oldState, oldNotify, oldStop })
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	if err := Run([]string{"idle"}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyIfExists(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := copyIfExists(source, destination, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyIfExists(source, destination, false); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(destination); string(body) != "old" {
		t.Fatal("existing destination overwritten")
	}
	if err := copyIfExists(source, destination, true); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(destination); string(body) != "new" {
		t.Fatal("destination not updated")
	}
}

func TestClearBrowserLocksRemovesOnlySymlinks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"SingletonCookie", "SingletonLock", "SingletonSocket"} {
		if err := os.Symlink("stale", filepath.Join(dir, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	regular := filepath.Join(dir, "SingletonRegular")
	if err := os.WriteFile(regular, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clearBrowserLocks(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SingletonCookie", "SingletonLock", "SingletonSocket"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal(name, err)
		}
	}
	if body, err := os.ReadFile(regular); err != nil || string(body) != "keep" {
		t.Fatal("regular lock changed", err)
	}
}
