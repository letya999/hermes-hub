package runtime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run's argument gate, durationEnv parsing, the health marker check and
// Prepare's permission pass all fail closed on bad input.
func TestRuntimeEntrypointGuards(t *testing.T) {
	if err := Run(nil); err == nil {
		t.Fatal("empty args accepted")
	}
	if err := Run([]string{"bogus"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
	t.Setenv("HUB_CAPABILITY_MODE", "")
	if err := Run([]string{"verify-managed"}); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("verify-managed without mode: %v", err)
	}
	t.Setenv("HUB_CAPABILITY_MODE", "unmanaged")
	if err := Run([]string{"health"}); err == nil {
		t.Fatal("missing health marker accepted")
	}

	t.Setenv("HUB_TEST_DUR", "")
	if got := durationEnv("HUB_TEST_DUR", 5*time.Second); got != 5*time.Second {
		t.Fatalf("empty: %v", got)
	}
	t.Setenv("HUB_TEST_DUR", "250ms")
	if got := durationEnv("HUB_TEST_DUR", time.Second); got != 250*time.Millisecond {
		t.Fatalf("duration: %v", got)
	}
	t.Setenv("HUB_TEST_DUR", "30")
	if got := durationEnv("HUB_TEST_DUR", time.Second); got != 30*time.Second {
		t.Fatalf("seconds: %v", got)
	}
	t.Setenv("HUB_TEST_DUR", "junk")
	if got := durationEnv("HUB_TEST_DUR", 7*time.Second); got != 7*time.Second {
		t.Fatalf("junk: %v", got)
	}
	if got := env("HUB_MISSING_ENV_XYZ", "fb"); got != "fb" {
		t.Fatalf("env fallback: %v", got)
	}
}

// Health reads the runtime marker: missing/garbage files fail, dead pids
// fail, and the live pid of this test satisfies the check.
func TestHealthMarker(t *testing.T) {
	dir := t.TempDir()
	markerPath := filepath.Join(dir, "runtime.json")
	if err := Health(markerPath); err == nil {
		t.Fatal("missing marker healthy")
	}
	if err := os.WriteFile(markerPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(markerPath); err == nil {
		t.Fatal("garbage marker healthy")
	}
	dead, _ := json.Marshal(marker{PIDs: []int{2_000_000_000 - 1}})
	if err := os.WriteFile(markerPath, dead, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(markerPath); err == nil {
		t.Fatal("dead pid healthy")
	}
	live, _ := json.Marshal(marker{PIDs: []int{os.Getpid()}})
	if err := os.WriteFile(markerPath, live, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Health(markerPath); err != nil {
		t.Fatalf("live pid unhealthy: %v", err)
	}
}

// Prepare walks the roots applying the supplied chown and permissions;
// private files get 0600, symlinks and special files are skipped.
func TestPreparePermissions(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	priv := filepath.Join(root, "self-env.json")
	if err := os.WriteFile(priv, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	var chowned []string
	err := Prepare([]string{root}, 10001, 1000, func(path string, uid, gid int) error {
		if uid != 10001 || gid != 1000 {
			t.Fatalf("chown ids: %d %d", uid, gid)
		}
		chowned = append(chowned, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(chowned) < 3 {
		t.Fatalf("chowned paths: %v", chowned)
	}
	failing := errors.New("denied")
	if err := Prepare([]string{root}, 1, 1, func(string, int, int) error { return failing }); err == nil {
		t.Fatal("chown failure swallowed")
	}
}
