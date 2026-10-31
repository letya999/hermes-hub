package agenttools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/letya999/hermes-hub/internal/exposure"
)

func exposureTestConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `schema: 1
provider: sish
relay:
  host: tunnel.example.com
  user: alice
  domain: hub.example.com
  host_keys:
    - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX"
zone: alice
key_ref: "broker:grant-01"
endpoints:
  onboarding:
    target: hub_public_listener
  demo:
    target: "service:mysite:8080"
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeProvider stands in for the tunnel workload behind the Provider seam.
type fakeProvider struct {
	mu     sync.Mutex
	opened map[string]exposure.Spec
	fail   bool
	closed int
	next   int
}

func newFakeProvider() *fakeProvider {
	return &fakeProvider{opened: map[string]exposure.Spec{}}
}

func (f *fakeProvider) Open(_ context.Context, spec exposure.Spec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return "", fmt.Errorf("provider down")
	}
	f.next++
	handle := fmt.Sprintf("h-%d", f.next)
	f.opened[handle] = spec
	return handle, nil
}

func (f *fakeProvider) Close(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.opened[handle]; !ok {
		return fmt.Errorf("unknown handle")
	}
	delete(f.opened, handle)
	f.closed++
	return nil
}

func (f *fakeProvider) live() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.opened)
}

func exposureFixture(t *testing.T) (*Tools, *fakeProvider) {
	t.Helper()
	config := exposureTestConfig(t)
	t.Setenv("HUB_EXPOSURE_CONFIG", config)
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	v := fixture(t)
	provider := newFakeProvider()
	v.setExposureProvider(provider)
	v.openExposure()
	return v, provider
}

func TestExposureOpenViaToolCall(t *testing.T) {
	v, provider := exposureFixture(t)
	out, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(map[string]any)
	if !ok || res["public_name"] != "demo.alice.hub.example.com" {
		t.Fatalf("open result: %+v", out)
	}
	if provider.live() != 1 {
		t.Fatal("provider workload did not start")
	}
	var spec exposure.Spec
	for _, s := range provider.opened {
		spec = s
	}
	if spec.Bind != "demo.alice" || spec.Target != "service:mysite:8080" {
		t.Fatalf("bind/target must come from config only: %+v", spec)
	}
}

func TestExposureListStatusCloseViaToolCall(t *testing.T) {
	v, provider := exposureFixture(t)
	ctx := context.Background()
	if _, err := v.Call(ctx, "exposure_open", Input{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	list, err := v.Call(ctx, "exposure_list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	tunnels, ok := list.(map[string]any)["tunnels"].([]map[string]any)
	if !ok || len(tunnels) != 1 || tunnels[0]["public_name"] != "demo.alice.hub.example.com" {
		t.Fatalf("list: %+v", list)
	}
	status, err := v.Call(ctx, "exposure_status", Input{Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	if status.(map[string]any)["public_name"] != "demo.alice.hub.example.com" {
		t.Fatalf("status: %+v", status)
	}
	if _, err = v.Call(ctx, "exposure_status", Input{Name: "onboarding"}); err == nil {
		t.Fatal("status of a closed endpoint must fail")
	}
	if _, err = v.Call(ctx, "exposure_close", Input{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	if provider.live() != 0 {
		t.Fatal("provider workload still running after close")
	}
}

func TestExposureBrokenConfigFailsClosed(t *testing.T) {
	broken := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(broken, []byte("schema: 9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_EXPOSURE_CONFIG", broken)
	t.Setenv("HUB_STATE", t.TempDir())
	v := fixture(t)
	if !toolNames(t, v)["exposure_open"] {
		t.Fatal("broken config must keep tools visible")
	}
	if _, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"}); err == nil {
		t.Fatal("broken config must fail closed, not panic")
	}
}

func TestExposureReadOnlyDeniesWrites(t *testing.T) {
	config := exposureTestConfig(t)
	t.Setenv("HUB_EXPOSURE_CONFIG", config)
	t.Setenv("HUB_TOOLS_RO", "exposure")
	t.Setenv("HUB_STATE", t.TempDir())
	v := fixture(t)
	v.setExposureProvider(newFakeProvider())
	v.openExposure()
	if _, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatal("write-effect tool under a read-only capability must refuse:", err)
	}
	if _, err := v.Call(context.Background(), "exposure_close", Input{Name: "demo"}); err == nil ||
		!strings.Contains(err.Error(), "read-only") {
		t.Fatal("exposure_close under a read-only capability must refuse:", err)
	}
	if _, err := v.Call(context.Background(), "exposure_list", Input{}); err != nil {
		t.Fatal("read-effect tool must stay usable:", err)
	}
}

func TestExposureOpenWritesAuditReceipt(t *testing.T) {
	v, _ := exposureFixture(t)
	if _, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(v.StateDir, "audit", "exposure.jsonl"))
	if err != nil {
		t.Fatal("exposure audit ledger missing:", err)
	}
	if !strings.Contains(string(body), "\"open\"") || !strings.Contains(string(body), "demo.alice.hub.example.com") {
		t.Fatalf("open receipt missing: %s", body)
	}
}

func TestExposureCloseStopsReaperGoroutine(t *testing.T) {
	v, _ := exposureFixture(t)
	reaperRunning := func() bool {
		buf := make([]byte, 1<<20)
		return strings.Contains(string(buf[:runtime.Stack(buf, true)]),
			"internal/exposure.NewManager")
	}
	if !reaperRunning() {
		t.Fatal("manager reaper should be running before Close")
	}
	v.Close()
	if reaperRunning() {
		t.Fatal("exposure reaper goroutine still running after Tools.Close")
	}
}

func TestExposureDefaultProviderBuildsManager(t *testing.T) {
	config := exposureTestConfig(t)
	t.Setenv("HUB_EXPOSURE_CONFIG", config)
	t.Setenv("HUB_STATE", t.TempDir())
	v := fixture(t)
	if v.Exposure == nil {
		t.Fatal("manager must build without an injected provider")
	}
	if v.exposureProvider == nil {
		t.Fatal("openExposure must construct the real sish provider when none is injected")
	}
	if _, err := v.Call(context.Background(), "exposure_open", Input{Name: "demo"}); err == nil {
		t.Fatal("unreachable relay must fail the open, not panic")
	}
	v.Close()
	if err := os.RemoveAll(filepath.Dir(os.Getenv("HUB_EXPOSURE_CONFIG"))); err != nil {
		t.Fatal("config root still held after close:", err)
	}
}

func TestExposureToolsAreOptIn(t *testing.T) {
	base := fixture(t)
	for name := range toolNames(t, base) {
		if strings.HasPrefix(name, "exposure_") {
			t.Fatal("exposure tool registered without the feature:", name)
		}
	}
	v, _ := exposureFixture(t)
	names := toolNames(t, v)
	for _, want := range []string{"exposure_list", "exposure_status", "exposure_open", "exposure_close"} {
		if !names[want] {
			t.Fatal("exposure tool missing:", want)
		}
	}
}
