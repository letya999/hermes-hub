package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SPEC-0053 R1.4/A1.4: runtime readiness compares the projected tool surface
// against the rendered manifest and names what is missing.
func TestSurfaceProbeReportsMissingProjectedNames(t *testing.T) {
	connectors := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/connectors" || r.Header.Get("Authorization") != "Bearer runtime-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "hub-agent-tools", "tools": []string{"code_exec"}},
			{"definition_id": "gitlab-mcp", "tools": []string{"search"}},
		}})
	}))
	defer connectors.Close()
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "good-model"}}})
	}))
	defer models.Close()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "model:\n  default: good-model\n  base_url: " + models.URL + "/v1\nmcp_servers:\n  toolhub:\n    url: " + connectors.URL + "/mcp\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema":1,"context":"alice","policy_version":"policy-x","projected":["terminal","image_gen","gitlab"],"direct":["web"]}`
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	t.Setenv("OPENAI_API_KEY", "model-key")

	result := surfaceProbe(context.Background())
	if result.surface != "degraded" {
		t.Fatalf("surface = %q, want degraded: %+v", result.surface, result)
	}
	if len(result.missing) != 1 || result.missing[0] != "image_gen" {
		t.Fatalf("missing = %v, want [image_gen]", result.missing)
	}
	if result.model != "ok" {
		t.Fatalf("model = %q, want ok", result.model)
	}
}

// SPEC-0053 R1.4: a space predating manifests (or a missing mount) cannot
// invent missing tools — readiness reports not_probed, never a degraded guess.
func TestSurfaceProbeAbsentManifestNotProbed(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "absent.json"))
	t.Setenv("HUB_TOOLHUB_ENDPOINT", "")
	result := surfaceProbe(context.Background())
	if result.surface != "not_probed" || len(result.missing) != 0 {
		t.Fatalf("absent manifest must not fabricate a degraded surface: %+v", result)
	}
}

// SPEC-0053 R1.5: an unresolved identity/plumbing env reference in the hub
// stdio child config degrades readiness with a reason; optional secrets left
// unset (HH_TOKEN) never flag the child broken.
func TestSurfaceProbeFlagsUnresolvedChildEnv(t *testing.T) {
	connectors := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "hub-agent-tools", "tools": []string{"code_exec"}},
		}})
	}))
	defer connectors.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "mcp_servers:\n  hub:\n    command: /usr/local/bin/hubctl\n    env:\n      HUB_POLICY_VERSION: ${HUB_POLICY_VERSION}\n      HH_TOKEN: ${HH_TOKEN}\n  toolhub:\n    url: " + connectors.URL + "/mcp\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(`{"schema":1,"context":"alice","policy_version":"policy-x","projected":["terminal"],"direct":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")

	result := surfaceProbe(context.Background())
	if result.surface != "degraded" {
		t.Fatalf("surface = %q, want degraded", result.surface)
	}
	if result.reasons["hub"] != "child_env_unresolved:HUB_POLICY_VERSION" {
		t.Fatalf("reasons = %v", result.reasons)
	}
	if len(result.missing) != 0 {
		t.Fatalf("missing = %v, ToolHub still serves terminal", result.missing)
	}
}

// SPEC-0053 R1.5: beyond env resolution, the runtime proves the hub stdio
// child actually starts and answers an MCP initialize — a missing or broken
// binary degrades readiness with a spawn reason, not a guess.
func TestSurfaceProbeHubChildSpawnFails(t *testing.T) {
	connectors := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "hub-agent-tools", "tools": []string{"code_exec"}},
		}})
	}))
	defer connectors.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "mcp_servers:\n  hub:\n    command: " + filepath.Join(dir, "absent-hubctl") + "\n    args: [tools]\n    env:\n      HUB_POLICY_VERSION: ${HUB_POLICY_VERSION}\n  toolhub:\n    url: " + connectors.URL + "/mcp\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(`{"schema":1,"context":"alice","policy_version":"policy-x","projected":["terminal"],"direct":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	t.Setenv("HUB_POLICY_VERSION", "policy-x")

	result := surfaceProbe(context.Background())
	if result.surface != "degraded" {
		t.Fatalf("surface = %q, want degraded", result.surface)
	}
	if result.reasons["hub"] != "child_spawn_failed" {
		t.Fatalf("reasons = %v", result.reasons)
	}
}

// SPEC-0053 R1.5: a child that spawns but dies before answering is a distinct
// signal from a missing binary — the classic unresolved-env death.
func TestSurfaceProbeHubChildDiesOnInit(t *testing.T) {
	connectors := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "hub-agent-tools", "tools": []string{"code_exec"}},
		}})
	}))
	defer connectors.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	// The test binary re-execs itself with HUB_SELFTEST_CHILD=die-fast; the
	// helper exits immediately, mirroring a stdio child killed by bad env.
	config := "mcp_servers:\n  hub:\n    command: " + filepath.ToSlash(mustAbs(t, os.Args[0])) + "\n    args: [-test.run=TestHelperProcessDieFast]\n    env:\n      HUB_POLICY_VERSION: ${HUB_POLICY_VERSION}\n  toolhub:\n    url: " + connectors.URL + "/mcp\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(`{"schema":1,"context":"alice","policy_version":"policy-x","projected":["terminal"],"direct":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	t.Setenv("HUB_POLICY_VERSION", "policy-x")
	t.Setenv("HUB_SELFTEST_CHILD", "die-fast")
	// Race instrumentation otherwise delays os.Exit by one second in the child.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	// The production grace window (1.5s) loses the race against a re-exec'd
	// instrumented test binary booting under parallel -race load: the parent
	// then kills a still-booting child and reports it healthy. The death
	// signature being proven is instant once the child runs, so only the boot
	// time is load-dependent — widen the window for this test.
	oldGrace := hubChildGrace
	hubChildGrace = 30 * time.Second
	t.Cleanup(func() { hubChildGrace = oldGrace })

	result := surfaceProbe(context.Background())
	if result.surface != "degraded" {
		t.Fatalf("surface = %q, want degraded", result.surface)
	}
	if result.reasons["hub"] != "child_init_failed" {
		t.Fatalf("reasons = %v", result.reasons)
	}
}

// TestHelperProcessDieFast is not a test: re-exec'd as the hub stdio child it
// exits immediately, mirroring a spawn that dies on its first handshake.
func TestHelperProcessDieFast(t *testing.T) {
	if os.Getenv("HUB_SELFTEST_CHILD") != "die-fast" {
		return
	}
	os.Exit(0)
}

// TestHelperProcessDiesAfterBoot is not a test: re-exec'd as the hub stdio
// child it outlives the production grace window, then exits — proof that the
// init verdict comes from the configured window, not a hardcoded one.
func TestHelperProcessDiesAfterBoot(t *testing.T) {
	if os.Getenv("HUB_SELFTEST_CHILD") != "die-after-boot" {
		return
	}
	time.Sleep(2 * time.Second)
	os.Exit(0)
}

// The spawn verdict must read hubChildGrace: a child that dies after the
// production 1.5s window but inside a widened one is still init-failed. If
// spawnHubChild hardcodes 1500ms it kills the child first and reports "".
func TestSpawnHubChildHonorsGraceWindow(t *testing.T) {
	old := hubChildGrace
	hubChildGrace = 60 * time.Second
	t.Cleanup(func() { hubChildGrace = old })
	t.Setenv("HUB_SELFTEST_CHILD", "die-after-boot")
	if reason := spawnHubChild(mustAbs(t, os.Args[0]), []string{"-test.run=TestHelperProcessDiesAfterBoot"}); reason != "child_init_failed" {
		t.Fatalf("reason = %q, want child_init_failed", reason)
	}
}

// SPEC-0053 R1.5: the spawn self-test is a startup proof — it must not fork a
// child on every health tick. Two probes over the same rendered command reuse
// the first result.
func TestSurfaceProbeHubChildSelfTestRunsOnce(t *testing.T) {
	connectors := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"connectors": []map[string]any{
			{"definition_id": "hub-agent-tools", "tools": []string{"code_exec"}},
		}})
	}))
	defer connectors.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	countFile := filepath.Join(dir, "spawns")
	config := "mcp_servers:\n  hub:\n    command: " + filepath.ToSlash(mustAbs(t, os.Args[0])) + "\n    args: [-test.run=TestHelperProcessStayAlive, " + filepath.ToSlash(countFile) + "]\n    env:\n      HUB_POLICY_VERSION: ${HUB_POLICY_VERSION}\n  toolhub:\n    url: " + connectors.URL + "/mcp\n    headers:\n      Authorization: Bearer ${HUB_RUNTIME_AUTH}\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(`{"schema":1,"context":"alice","policy_version":"policy-x","projected":["terminal"],"direct":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("HUB_RUNTIME_AUTH", "runtime-token")
	t.Setenv("HUB_POLICY_VERSION", "policy-x")
	t.Setenv("HUB_SELFTEST_CHILD", "stay-alive")

	surfaceProbe(context.Background())
	result := surfaceProbe(context.Background())
	if result.surface != "ok" {
		t.Fatalf("surface = %q, want ok: %+v", result.surface, result)
	}
	body, err := os.ReadFile(countFile)
	if err != nil || strings.Count(string(body), "\n") != 1 {
		t.Fatalf("self-test spawned more than once: %q %v", body, err)
	}
}

// TestHelperProcessStayAlive is not a test: re-exec'd as the hub stdio child
// it records the spawn and idles like a real stdio server awaiting input.
func TestHelperProcessStayAlive(t *testing.T) {
	if os.Getenv("HUB_SELFTEST_CHILD") != "stay-alive" {
		return
	}
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-test.run=") || strings.HasPrefix(arg, "-test.") {
			continue
		}
		if strings.HasSuffix(arg, "spawns") {
			if f, err := os.OpenFile(arg, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
				_, _ = f.WriteString("spawned\n")
				_ = f.Close()
			}
		}
	}
	time.Sleep(10 * time.Second)
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// SPEC-0053 R1.6/A1.6: a configured model absent from the provider catalog is
// the distinct model_not_found signal /status surfaces to the owner.
func TestSurfaceProbeModelNotFound(t *testing.T) {
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "other-model"}}})
	}))
	defer models.Close()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "hermes"), 0700); err != nil {
		t.Fatal(err)
	}
	config := "model:\n  default: absent-model\n  base_url: " + models.URL + "/v1\n"
	if err := os.WriteFile(filepath.Join(dir, "hermes", "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-manifest.json"), []byte(`{"schema":1,"context":"a","policy_version":"policy-x","projected":[],"direct":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_HOME", filepath.Join(dir, "hermes"))
	t.Setenv("HUB_TOOL_MANIFEST", filepath.Join(dir, "tool-manifest.json"))
	t.Setenv("OPENAI_API_KEY", "key")

	result := surfaceProbe(context.Background())
	if result.model != "model_not_found" {
		t.Fatalf("model = %q, want model_not_found", result.model)
	}
}
