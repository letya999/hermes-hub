package runtime

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type prepareFileInfo struct {
	name string
	mode fs.FileMode
}

func (i prepareFileInfo) Name() string       { return i.name }
func (i prepareFileInfo) Size() int64        { return 0 }
func (i prepareFileInfo) Mode() fs.FileMode  { return i.mode }
func (i prepareFileInfo) ModTime() time.Time { return time.Time{} }
func (i prepareFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i prepareFileInfo) Sys() any           { return nil }

func TestPrepareEntrySkipsSpecialFiles(t *testing.T) {
	entry := fs.FileInfoToDirEntry(prepareFileInfo{name: "runtime.sock", mode: os.ModeSocket})
	called := false
	err := prepareEntry("runtime.sock", entry, false, 1, 2, func(string, int, int) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("prepareEntry touched a special file")
	}
}

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

func TestPrepareKeepsGlabConfigPrivate(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("Windows does not preserve Unix permission bits")
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "home", ".config", "glab-cli")
	if err := os.MkdirAll(configDir, 0770); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yml", "aliases.yml"} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte("token: redacted\n"), 0660); err != nil {
			t.Fatal(err)
		}
	}
	if err := Prepare([]string{root}, 1, 2, func(string, int, int) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yml", "aliases.yml"} {
		info, err := os.Stat(filepath.Join(configDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestHealthAndRunValidation(t *testing.T) {
	configureProcess(&exec.Cmd{})
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
	t.Setenv("ATLASSIAN_EMAIL", "owner@example.com")
	t.Setenv("ATLASSIAN_API_TOKEN", "token")
	t.Setenv("ATLASSIAN_BASIC_AUTH", "")
	t.Cleanup(func() { state = oldState })
	if err := os.WriteFile(filepath.Join(state, "self-env.json"), []byte(`{"GITHUB_TOKEN":"updated","ATLASSIAN_EMAIL":"old@example.com","ATLASSIAN_API_TOKEN":"old-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfEnv(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("GITHUB_TOKEN") != "updated" {
		t.Fatal("self env was not loaded")
	}
	if os.Getenv("ATLASSIAN_BASIC_AUTH") != "" {
		t.Fatal("legacy Rovo auth was derived")
	}
	body, err := os.ReadFile(filepath.Join(state, "self-env.json"))
	if err != nil || strings.Contains(string(body), "ATLASSIAN_") {
		t.Fatal("legacy Atlassian values were not removed")
	}
	if err := os.WriteFile(filepath.Join(state, "self-env.json"), []byte(`{"ORG_TOKEN":"blocked"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfEnv(); err == nil {
		t.Fatal("protected self env was loaded")
	}
}

func TestApplySelfServicesMergesMCPConfig(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(`{"features":["atlassian","gitlab"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(config, []byte("model: {}\nmcp_servers: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServices(config); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	// Connectors are ToolHub-managed: enabling them must not inject a direct
	// MCP definition into the Hermes config.
	if strings.Contains(text, "mcp-atlassian") || strings.Contains(text, "mcp.atlassian.com") || strings.Contains(text, "gitlab") {
		t.Fatal(text)
	}
}

func TestLoadSelfServicesSetsActiveFeatures(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	t.Setenv("HUB_FEATURES", "workspace")
	t.Setenv("HUB_HH_ENABLED", "false")
	if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(`{"features":["hh","gitlab"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfServices(); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("HUB_HH_ENABLED") != "true" || !strings.Contains(os.Getenv("HUB_ACTIVE_FEATURES"), "gitlab") {
		t.Fatal(os.Getenv("HUB_ACTIVE_FEATURES"), os.Getenv("HUB_HH_ENABLED"))
	}
}

func TestLoadSelfServicesRejectsInvalidState(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	for _, body := range []string{`not-json`, `{"features":["browser"]}`, `{"features":["gitlab","gitlab"]}`} {
		if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := loadSelfServices(); err == nil {
			t.Fatal("invalid self-services accepted", body)
		}
	}
}

func TestSelfServicesRejectsInvalidState(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	for _, body := range []string{`not-json`, `{"features":["workspace"]}`, `{"features":["gitlab","gitlab"]}`} {
		if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSelfServices(); err == nil {
			t.Fatal("invalid self-services accepted", body)
		}
	}
	if err := os.Remove(filepath.Join(state, selfServicesFile)); err != nil {
		t.Fatal(err)
	}
	if features, err := readSelfServices(); err != nil || features != nil {
		t.Fatal(features, err)
	}
	if err := os.Mkdir(filepath.Join(state, selfServicesFile), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSelfServices(); err == nil {
		t.Fatal("directory self-services accepted")
	}
}

func TestLoadSelfServicesMergesConfiguredFeatures(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(`{"features":["gitlab"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_FEATURES", "workspace")
	if err := loadSelfServices(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("HUB_ACTIVE_FEATURES"); !strings.Contains(got, "workspace") || !strings.Contains(got, "gitlab") {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(`{"features":["hh"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := loadSelfServices(); err != nil || os.Getenv("HUB_HH_ENABLED") != "true" {
		t.Fatal(err, os.Getenv("HUB_HH_ENABLED"))
	}
}

func TestApplySelfServicesNoopAndConfigErrors(t *testing.T) {
	oldState := state
	state = t.TempDir()
	t.Cleanup(func() { state = oldState })
	if err := applySelfServices(filepath.Join(state, "missing.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, selfServicesFile), []byte(`{"features":["github"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(state, "bad.yaml")
	if err := os.WriteFile(bad, []byte("not: [yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServices(bad); err == nil {
		t.Fatal("invalid Hermes config accepted")
	}
	noServers := filepath.Join(state, "no-servers.yaml")
	if err := os.WriteFile(noServers, []byte("model: test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applySelfServices(noServers); err != nil {
		t.Fatal(err)
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
	if err := Run([]string{"gateway"}); err == nil {
		t.Fatal("missing Hermes executable accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "runtime.json")); !os.IsNotExist(err) {
		t.Fatal("runtime marker left behind")
	}
	if err := Run([]string{"serve"}); err == nil {
		t.Fatal("runtime server started without authorization")
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
	command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeExitHelper")
		cmd.Env = append(os.Environ(), "HUB_TEST_EXIT_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { state, command = oldState, oldCommand })
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("gateway"); err == nil {
		t.Fatal("child exit accepted")
	}
}

func TestSupervisorUsesConfiguredHermesAndHomeDirectories(t *testing.T) {
	oldState, oldCommand := state, command
	state = t.TempDir()
	root := t.TempDir()
	hermesHome, home := filepath.Join(root, "hermes"), filepath.Join(root, "home")
	t.Setenv("HERMES_HOME", hermesHome)
	t.Setenv("HOME", home)
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeExitHelper")
		cmd.Env = append(os.Environ(), "HUB_TEST_EXIT_HELPER=1")
		return cmd
	}
	t.Cleanup(func() { state, command = oldState, oldCommand })
	if err := supervise("gateway"); err == nil {
		t.Fatal("child exit accepted")
	}
	for _, path := range []string{home, hermesHome, filepath.Join(hermesHome, "skills"), filepath.Join(hermesHome, "memories")} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("configured runtime directory missing: %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "hermes")); !os.IsNotExist(err) {
		t.Fatal("created a second Hermes home under runtime state")
	}
}

func TestSupervisorBrowserShutdown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	oldState, oldCommand, oldURL := state, command, browserURL
	oldNotify, oldStop := signalNotify, signalStop
	state, browserURL = t.TempDir(), server.URL
	command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperProcess")
		cmd.Env = append(os.Environ(), "HUB_TEST_HELPER=1")
		return cmd
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

func TestRuntimeHelperProcess(t *testing.T) {
	if os.Getenv("HUB_TEST_HELPER") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
	os.Exit(0)
}

func TestRuntimeExitHelper(t *testing.T) {
	if os.Getenv("HUB_TEST_EXIT_HELPER") == "1" {
		os.Exit(3)
	}
}

func TestRunIdleShutdown(t *testing.T) {
	oldState, oldWorkspace := state, workspace
	oldNotify, oldStop := signalNotify, signalStop
	oldChown := chown
	state, workspace = filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "workspace")
	signalNotify = func(ch chan os.Signal) { ch <- os.Interrupt }
	signalStop = func(chan os.Signal) {}
	chown = func(string, int, int) error { return nil }
	t.Cleanup(func() {
		state, workspace, signalNotify, signalStop, chown = oldState, oldWorkspace, oldNotify, oldStop, oldChown
	})
	t.Setenv("HUB_SHARED_GID", "1000")
	if err := Run([]string{"prepare"}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Run([]string{"idle"}) }()
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

func TestServeStartsPinnedGatewayChild(t *testing.T) {
	oldState, oldCommand, oldNotify, oldStop := state, command, signalNotify, signalStop
	state = t.TempDir()
	command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=TestRuntimeHelperProcess")
		cmd.Env = append(os.Environ(), "HUB_TEST_HELPER=1")
		return cmd
	}
	signalNotify = func(ch chan os.Signal) { ch <- os.Interrupt }
	signalStop = func(chan os.Signal) {}
	t.Cleanup(func() { state, command, signalNotify, signalStop = oldState, oldCommand, oldNotify, oldStop })
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-secret")
	t.Setenv("HUB_PERSISTENT_HERMES", "false")
	t.Setenv("HUB_BROWSER", "false")
	t.Setenv("HUB_MEET", "false")
	if err := supervise("serve"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "runtime.json")); !os.IsNotExist(err) {
		t.Fatal("runtime marker was not cleaned")
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

func TestClearDisplayLocksRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "X99-lock"), filepath.Join(dir, "X99")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := clearDisplayLocks(paths...); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale display lock remains: %s", path)
		}
	}
}
