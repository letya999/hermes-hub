package stack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runtimeBinds(t *testing.T, s Settings, dir string) []M {
	t.Helper()
	out := Compose(s, t.TempDir(), dir)
	rt, ok := out["services"].(M)["hermes-runtime"].(M)
	if !ok {
		t.Skip("no hermes-runtime service in this render")
	}
	vols, _ := rt["volumes"].([]any)
	var binds []M
	for _, v := range vols {
		if m, ok := v.(M); ok {
			binds = append(binds, m)
		}
	}
	return binds
}

// S1.4: once a revision is published the runtime mounts the passport view as
// one read-only directory at /config — no single-file /config binds remain —
// and nothing writable can reach the passport authority dirs.
func TestComposeConfigViewDirBindOnly(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated", "config-view", "dev"), 0755); err != nil {
		t.Fatal(err)
	}
	binds := runtimeBinds(t, s, dir)
	var viewBinds int
	for _, m := range binds {
		src := filepath.FromSlash(fmt.Sprint(m["source"]))
		target := fmt.Sprint(m["target"])
		ro := m["read_only"] == true
		if target == "/config" {
			viewBinds++
			if !ro {
				t.Fatal("/config bind is not read-only")
			}
			if !strings.HasSuffix(src, filepath.Join("generated", "config-view", "dev")) {
				t.Fatalf("/config bind does not point at config-view: %s", src)
			}
		}
		if strings.HasPrefix(target, "/config/") && !ro {
			t.Fatalf("nested /config bind not read-only: %s", target)
		}
		// Writable binds must never expose the passport authority dirs
		// (generated/skills-effective is a deliberate agent-writable view).
		if !ro && (strings.Contains(src, filepath.Join("generated", "passports")) ||
			strings.Contains(src, filepath.Join("generated", "config-view")) ||
			strings.Contains(src, "passport-src")) {
			t.Fatalf("writable bind reaches passport authority: %s -> %s", src, target)
		}
	}
	if viewBinds != 1 {
		t.Fatalf("want exactly one /config dir bind, got %d", viewBinds)
	}
	// And the stale single-file binds are gone when the view exists.
	for _, m := range binds {
		if fmt.Sprint(m["target"]) == "/config/config.yaml" || fmt.Sprint(m["target"]) == "/config/SOUL.md" {
			t.Fatalf("stale single-file bind survived: %v", m["target"])
		}
	}
}

// Before any revision is published the space keeps the raw env config and
// SOUL mounts its heal path reads — the view bind appears only then.
func TestComposeConfigViewFallbackBeforePublish(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", OAuthPort: 8000, BrowserPort: 6080}
	binds := runtimeBinds(t, s, t.TempDir())
	var cfg, soul bool
	for _, m := range binds {
		target := fmt.Sprint(m["target"])
		if target == "/config" {
			t.Fatal("dir bind present without a published view")
		}
		if target == "/config/config.yaml" {
			cfg = m["read_only"] == true
		}
		if target == "/config/SOUL.md" {
			soul = m["read_only"] == true
		}
	}
	if !cfg || !soul {
		t.Fatalf("unpublished space lost its heal-path mounts: cfg=%v soul=%v", cfg, soul)
	}
}
