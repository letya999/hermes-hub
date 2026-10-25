package main

// The cellinit contract is the cell's pid1 and canary agent: these tests pin
// the file verbs and the cred-proxy allowlist the controller depends on.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "canary")
	if err := writeFile([]string{target, "tok123"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "tok123" {
		t.Fatalf("writeFile content=%q err=%v", data, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(target)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("canary mode=%v", info.Mode().Perm())
		}
	}
	if err := writeFile(nil); err == nil {
		t.Fatal("write with no args succeeded")
	}
	if err := writeFile([]string{"a", "b", "c"}); err == nil {
		t.Fatal("write with three args succeeded")
	}
}

func TestWriteFileStdin(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "seed")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("stdin-content"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	if err := writeFile([]string{target}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "stdin-content" {
		t.Fatalf("stdin write content=%q", data)
	}
}

// Cred-proxy rules can reach ~270 KiB (16 grants x 16 KiB values): stdin
// delivery must accept that, not just canary-sized payloads.
func TestWriteFileStdinLarge(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "rules")
	payload := strings.Repeat("x", 200*1024)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		_, _ = w.WriteString(payload)
		_ = w.Close()
	}()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old; r.Close() }()
	if err := writeFile([]string{target}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(target)
	if len(data) != len(payload) {
		t.Fatalf("stdin write truncated: %d want %d", len(data), len(payload))
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "canary")
	if err := os.WriteFile(target, []byte("tok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := check([]string{target, "tok"}); err != nil {
		t.Fatalf("matching canary denied: %v", err)
	}
	if err := check([]string{target, "other"}); err == nil {
		t.Fatal("canary mismatch passed")
	}
	if err := check([]string{filepath.Join(dir, "missing"), "tok"}); err == nil {
		t.Fatal("missing canary passed")
	}
	if err := check([]string{target}); err == nil {
		t.Fatal("check with one arg passed")
	}
}

func TestClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cell-canary"), []byte("tok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clean([]string{dir, ".cell-canary", "tok"}); err != nil {
		t.Fatalf("clean dir denied: %v", err)
	}
	// A stray file makes the cell dirty.
	if err := os.WriteFile(filepath.Join(dir, "leftover"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := clean([]string{dir, ".cell-canary", "tok"}); err == nil {
		t.Fatal("dirty dir passed clean")
	}
	if err := os.Remove(filepath.Join(dir, "leftover")); err != nil {
		t.Fatal(err)
	}
	// Wrong token still fails even with the right shape.
	if err := clean([]string{dir, ".cell-canary", "WRONG"}); err == nil {
		t.Fatal("token mismatch passed clean")
	}
	// A directory entry named like the canary is not the canary.
	if err := os.Remove(filepath.Join(dir, ".cell-canary")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".cell-canary"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := clean([]string{dir, ".cell-canary", "tok"}); err == nil {
		t.Fatal("canary directory passed clean")
	}
	if err := clean([]string{dir, ".cell-canary"}); err == nil {
		t.Fatal("clean with two args passed")
	}
	if err := clean([]string{filepath.Join(dir, "gone"), "x", "y"}); err == nil {
		t.Fatal("missing dir passed clean")
	}
}

func TestStrayPIDsProcsSweep(t *testing.T) {
	// /proc only exists on unix cells; on Windows these must fail, not lie.
	stray, err := strayPIDs()
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("strayPIDs succeeded without /proc")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	self := strconv.Itoa(os.Getpid())
	for _, pid := range stray {
		if pid == "1" || pid == self {
			t.Fatalf("stray list contains %s", pid)
		}
	}
	_ = procs()
	// sweep SIGKILLs every pid outside pid1+self: only safe inside the cell's
	// own pid namespace (cellinit as pid1). On a shared host or CI runner the
	// same call would kill the machine — the production path reaches it
	// through `docker exec` inside the cell instead. killProc on a pid we own
	// is the safe subset: exercise it on a helper child only.
	helper := exec.Command(os.Args[0], "-test.run=TestPauseStopsOnSignal")
	helper.Env = append(os.Environ(), "CELLINIT_HELPER=pause")
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	killProc(helper.Process.Pid)
	if err := helper.Wait(); err == nil {
		t.Fatal("killed helper exited cleanly")
	}
	if os.Getpid() == 1 {
		_ = sweep()
	}
}

func TestReadProxyRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	body, _ := json.Marshal(map[string]map[string]string{
		"API.Example.COM": {"Authorization": "Bearer x"},
	})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	rules, err := readProxyRules(path)
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := rules["api.example.com"]
	if !ok || rule.Headers["Authorization"] != "Bearer x" || rule.Scheme != "https" {
		t.Fatalf("rules=%+v", rules)
	}
	if _, err := readProxyRules(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing rules file parsed")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readProxyRules(path); err == nil {
		t.Fatal("invalid rules json parsed")
	}
}

func TestProxyHandlerDeniesUnlistedHost(t *testing.T) {
	h := &proxyHandler{rules: map[string]proxyRule{}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://evil.example.com/", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unlisted host status=%d", rec.Code)
	}
}

func TestProxyHandlerPathHostAndUpstreamError(t *testing.T) {
	// A listed host that cannot be dialed: exercises the host extraction and
	// header injection, then reports the upstream failure honestly.
	h := &proxyHandler{rules: map[string]proxyRule{
		"localhost": {Headers: map[string]string{"Authorization": "Bearer x"}, Scheme: "http"},
	}}
	rec := httptest.NewRecorder()
	// No URL host: the first path segment carries it.
	req := httptest.NewRequest(http.MethodGet, "http://proxy/localhost/v1/data", nil)
	req.URL.Host = ""
	req.URL.Path = "/localhost/v1/data"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable upstream status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCredProxyArgs(t *testing.T) {
	if err := credProxy(nil); err == nil {
		t.Fatal("cred-proxy without --rules started")
	}
	dir := t.TempDir()
	if err := credProxy([]string{"--rules", filepath.Join(dir, "missing"), "--rules-timeout", "50ms"}); err == nil {
		t.Fatal("cred-proxy with missing rules started")
	} else if !strings.Contains(err.Error(), "cred-proxy: rules:") {
		t.Fatalf("missing rules did not fail through the wait path: %v", err)
	}
	rules := filepath.Join(dir, "rules.json")
	if err := os.WriteFile(rules, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := credProxy([]string{"--rules", rules, "--listen", "bad listen addr"}); err == nil {
		t.Fatal("cred-proxy on an invalid address started")
	}
}

// The controller injects rules into the running sidecar's tmpfs after start —
// the proxy must wait for the file instead of dying on a missing read.
func TestWaitProxyRulesArrivingLate(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.json")
	go func() {
		time.Sleep(80 * time.Millisecond)
		_ = os.WriteFile(rules, []byte(`{"h":{"Authorization":"Bearer x"}}`), 0600)
	}()
	got, err := waitProxyRules(rules, 5*time.Second)
	if err != nil || got["h"].Headers["Authorization"] != "Bearer x" {
		t.Fatalf("waitProxyRules=%v %v", got, err)
	}
	if _, err := waitProxyRules(filepath.Join(dir, "never"), 60*time.Millisecond); err == nil {
		t.Fatal("waitProxyRules returned without the file")
	}
}

// TestRunDispatch exercises run() in-process: the dispatch table and the
// failure exit paths are counted only when the test binary itself runs them.
// pause and sweep stay subprocess-only — one blocks for signals, the other
// SIGKILLs the whole pid namespace.
func TestRunDispatch(t *testing.T) {
	dir := t.TempDir()
	canary := filepath.Join(dir, "c")
	if err := os.WriteFile(canary, []byte("tok"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no-subcommand", []string{"cellinit"}, 1},
		{"unknown", []string{"cellinit", "bogus"}, 1},
		{"check-ok", []string{"cellinit", "check", canary, "tok"}, 0},
		{"check-mismatch", []string{"cellinit", "check", canary, "nope"}, 1},
		{"clean-bad-args", []string{"cellinit", "clean"}, 1},
		{"write-bad-args", []string{"cellinit", "write"}, 1},
		{"cred-proxy-no-rules", []string{"cellinit", "cred-proxy"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if rc := run(tc.args); rc != tc.want {
				t.Fatalf("run(%v)=%d want %d", tc.args, rc, tc.want)
			}
		})
	}
	// procs is read-only: safe in any pid namespace, exit code depends on how
	// many stray processes the runner happens to host.
	_ = run([]string{"cellinit", "procs"})
}

// TestMainDispatch runs the helper as a subprocess so main()/os.Exit wiring is
// exercised end to end.
func TestMainDispatch(t *testing.T) {
	if os.Getenv("CELLINIT_HELPER") == "1" {
		os.Args = []string{"cellinit"}
		if sub := os.Getenv("CELLINIT_SUB"); sub != "" {
			os.Args = append(os.Args, strings.Split(sub, "\x1f")...)
		}
		main()
		return
	}
	dir := t.TempDir()
	canary := filepath.Join(dir, "c")
	if err := os.WriteFile(canary, []byte("tok"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		sub     string
		wantErr bool
	}{
		{"no-subcommand", "", true},
		{"unknown", "bogus", true},
		{"check-ok", "check\x1f" + canary + "\x1ftok", false},
		{"check-mismatch", "check\x1f" + canary + "\x1fnope", true},
		{"clean-bad-args", "clean", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=TestMainDispatch")
			cmd.Env = append(os.Environ(), "CELLINIT_HELPER=1", "CELLINIT_SUB="+tc.sub)
			err := cmd.Run()
			if tc.wantErr && err == nil {
				t.Fatalf("subcommand %q succeeded", tc.sub)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("subcommand %q failed: %v", tc.sub, err)
			}
		})
	}
}

// TestPauseInProcess covers pause() and reapOrphans() directly: the signal
// channel swallows our own SIGTERM so the process survives. reapOrphans exits
// on ECHILD since the test process reaps its own children.
func TestPauseInProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery is not supported on Windows test runners")
	}
	self, _ := os.FindProcess(os.Getpid())
	done := make(chan error, 1)
	go func() { done <- pause() }()
	var err error
	for i := 0; i < 3; i++ {
		time.Sleep(150 * time.Millisecond)
		err = self.Signal(syscall.SIGTERM)
		select {
		case perr := <-done:
			if perr != nil {
				t.Fatalf("pause returned %v", perr)
			}
			return
		default:
		}
	}
	t.Fatalf("pause did not return after SIGTERM (kill err=%v)", err)
}

func TestPauseStopsOnSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM delivery is not supported on Windows test runners")
	}
	if os.Getenv("CELLINIT_HELPER") == "pause" {
		os.Args = []string{"cellinit", "pause"}
		main()
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestPauseStopsOnSignal")
	cmd.Env = append(os.Environ(), "CELLINIT_HELPER=pause")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pause did not exit cleanly on SIGTERM: %v", err)
	}
}
