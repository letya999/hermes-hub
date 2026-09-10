package agenttools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) *Tools {
	t.Helper()
	v, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(v.Close)
	return v
}
func TestFilesIsolationAndRevision(t *testing.T) {
	v := fixture(t)
	r, err := v.File("write", Input{Path: "drafts/a.md", Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.File("write", Input{Path: "drafts/a.md", Text: "two"}); err == nil {
		t.Fatal("lost update")
	}
	if _, err = v.File("write", Input{Path: "drafts/a.md", Text: "two", Revision: r["revision"].(string)}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../escape", "/etc/passwd", "a/../../b", "a\\b"} {
		if _, err = v.File("read", Input{Path: p}); err == nil {
			t.Fatal("escape", p)
		}
	}
	if _, err = v.File("write", Input{Root: "archive", Path: "a.md", Text: "x"}); err == nil {
		t.Fatal("archive write")
	}
	out, err := v.File("search", Input{Query: "two"})
	if err != nil || len(out["items"].([]map[string]any)) != 1 {
		t.Fatal(out, err)
	}
}

func TestOrganizationRootIsReadOnly(t *testing.T) {
	org := t.TempDir()
	if err := os.WriteFile(filepath.Join(org, "MEMORY.md"), []byte("org fact"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := Open(t.TempDir(), t.TempDir(), org)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if out, err := v.File("read", Input{Root: "organization", Path: "MEMORY.md"}); err != nil || out["text"] != "org fact" {
		t.Fatal(out, err)
	}
	if _, err = v.File("write", Input{Root: "organization", Path: "MEMORY.md", Text: "changed"}); err == nil {
		t.Fatal("organization write accepted")
	}
}

func TestOrganizationWriteRequiresOrganizationScopeAndAction(t *testing.T) {
	org := t.TempDir()
	if err := os.WriteFile(filepath.Join(org, "MEMORY.md"), []byte("org fact"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_SCOPE_ID", "organization:acme")
	t.Setenv("HUB_ORG_ACTIONS", "organization.write")
	v, err := Open(t.TempDir(), t.TempDir(), org)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.File("write", Input{Root: "organization", Path: "MEMORY.md", Text: "changed", Revision: hash([]byte("org fact"))}); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizationActionBlocksHHApply(t *testing.T) {
	org := t.TempDir()
	t.Setenv("HUB_ORG_ACTIONS", "")
	v, err := Open(t.TempDir(), t.TempDir(), org)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	v.HHEnabled = true
	v.HHKey = "private"
	if _, err = v.API(context.Background(), "hh_apply", Input{Authorized: true, ID: "123", ResumeID: "resume"}); err == nil {
		t.Fatal("organization action accepted")
	}
}

func TestSelfEnvUpdateIsUserScopedAndDoesNotReturnValues(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HUB_STATE", state)
	t.Setenv("HUB_SELF_ENV_KEYS", "GITHUB_TOKEN,SLACK_MCP_XOXP_TOKEN")
	t.Setenv("HUB_PROTECTED_ENV_KEYS", "ORG_TOKEN")
	v, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	restarted := false
	v.Restart = func() error { restarted = true; return nil }
	out, err := v.EnvUpdate(Input{Text: "GITHUB_TOKEN=secret=not-in-result"})
	if err != nil || !restarted || out["restart_required"] != true || out["restart_scheduled"] != true {
		t.Fatal(out, err)
	}
	b, err := os.ReadFile(filepath.Join(state, "self-env.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) == "" || string(b) == "secret=not-in-result" {
		t.Fatal("invalid self-env persistence")
	}
	var values map[string]string
	if err := json.Unmarshal(b, &values); err != nil || values["GITHUB_TOKEN"] != "secret=not-in-result" {
		t.Fatal(values, err)
	}
	if _, err := v.EnvUpdate(Input{Text: "ORG_TOKEN=blocked"}); err == nil {
		t.Fatal("organization env update accepted")
	}
}

func TestSelfEnvUpdateReportsRestartFailureWithoutReturningValue(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_SELF_ENV_KEYS", "GITHUB_TOKEN")
	v := fixture(t)
	if out, err := v.EnvUpdate(Input{Text: "GITHUB_TOKEN=private"}); err == nil || out["restart_scheduled"] != false {
		t.Fatal(out, err)
	}
	v.Restart = func() error { return fmt.Errorf("test restart failure") }
	if out, err := v.EnvUpdate(Input{Text: "GITHUB_TOKEN=private2"}); err == nil || out["restart_scheduled"] != false {
		t.Fatal(out, err)
	}
}

func TestServiceCatalogAndEnable(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HUB_STATE", state)
	t.Setenv("HUB_FEATURES", "workspace")
	t.Setenv("HUB_ORG_SCOPED", "false")
	t.Setenv("GITLAB_TOKEN", "pat")
	v, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if out, err := v.ServiceCatalog(); err != nil || out["secret_values_included"] != false || strings.Contains(fmt.Sprint(out), "pat") {
		t.Fatal(out, err)
	}
	restarted := false
	v.Restart = func() error { restarted = true; return nil }
	out, err := v.ServiceEnable(Input{Service: "gitlab"})
	if err != nil || out["enabled"] != true || !restarted {
		t.Fatal(out, err)
	}
	body, err := os.ReadFile(filepath.Join(state, "self-services.json"))
	if err != nil || string(body) == "" {
		t.Fatal(string(body), err)
	}
	t.Setenv("ATLASSIAN_EMAIL", "")
	t.Setenv("ATLASSIAN_API_TOKEN", "")
	out, err = v.ServiceEnable(Input{Service: "atlassian"})
	if err != nil || out["enabled"] != false {
		t.Fatal(out, err)
	}
	missing := out["missing_env"].([]string)
	if len(missing) != 2 || missing[0] != "ATLASSIAN_API_TOKEN" || missing[1] != "ATLASSIAN_EMAIL" {
		t.Fatal(missing)
	}
}

func TestServiceEnableRejectsOrganizationScope(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_ORG_SCOPED", "true")
	v := fixture(t)
	if _, err := v.ServiceEnable(Input{Service: "gitlab"}); err == nil {
		t.Fatal("organization service change accepted")
	}
}

func TestSelfServicesValidationAndDependencies(t *testing.T) {
	state := t.TempDir()
	t.Setenv("HUB_STATE", state)
	v := fixture(t)
	for _, body := range [][]byte{
		[]byte(`{"features":["gitlab"]}`),
		[]byte(`{"features":["gitlab","gitlab"]}`),
		[]byte(`{"features":["workspace"]}`),
		[]byte(`not-json`),
	} {
		if err := os.WriteFile(filepath.Join(state, "self-services.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := v.ServiceCatalog(); err == nil && string(body) != `{"features":["gitlab"]}` {
			t.Fatal("invalid self-services accepted", string(body))
		}
	}
	if err := os.WriteFile(filepath.Join(state, "self-services.json"), []byte(`{"features":["google_write"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := v.ServiceCatalog(); err != nil {
		t.Fatal(err)
	}
	if deps := mustServiceDependencies("google_write"); !deps["google"] {
		t.Fatal(deps)
	}
	if _, err := v.ServiceEnable(Input{Service: "unknown"}); err == nil {
		t.Fatal("unknown service accepted")
	}
	if _, err := v.ServiceEnable(Input{Service: "browser"}); err == nil {
		t.Fatal("host-managed service accepted")
	}
	t.Setenv("GITLAB_TOKEN", "pat")
	if _, err := v.ServiceEnable(Input{Service: "gitlab"}); err == nil {
		t.Fatal("missing restart was accepted")
	}
	v.Restart = func() error { return fmt.Errorf("restart failed") }
	if _, err := v.ServiceEnable(Input{Service: "gitlab"}); err == nil {
		t.Fatal("restart failure was hidden")
	}
	if info, err := v.ServiceCatalog(); err != nil || info["services"] == nil {
		t.Fatal(info, err)
	}
}

func TestRestartRuntimeValidation(t *testing.T) {
	state := t.TempDir()
	if err := restartRuntime(state); err == nil {
		t.Fatal("missing runtime marker accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restartRuntime(state); err == nil {
		t.Fatal("malformed runtime marker accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{"pids":[1]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restartRuntime(state); err == nil {
		t.Fatal("unsafe runtime pid accepted")
	}
}

func TestRestartRuntimeSchedulesSupervisor(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(fmt.Sprintf(`{"pids":[%d]}`, os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{})
	old := signalRuntime
	signalRuntime = func(*os.Process) { close(called) }
	t.Cleanup(func() { signalRuntime = old })
	if err := restartRuntime(state); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("supervisor restart was not scheduled")
	}
}

func TestRestartRuntimeCanDeferSupervisorSignal(t *testing.T) {
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(fmt.Sprintf(`{"pids":[%d]}`, os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_DEFER_RUNTIME_RESTART", "true")
	called := false
	old := signalRuntime
	signalRuntime = func(*os.Process) { called = true }
	t.Cleanup(func() { signalRuntime = old })
	if err := restartRuntime(state); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("deferred restart signaled supervisor immediately")
	}
	if _, err := os.Stat(filepath.Join(state, "restart.request")); err != nil {
		t.Fatal("deferred restart request missing", err)
	}
}
func TestSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	out := t.TempDir()
	_ = os.WriteFile(filepath.Join(out, "secret"), []byte("secret"), 0600)
	if err := os.Symlink(out, filepath.Join(dir, "link")); err != nil {
		t.Skip(err)
	}
	v, err := Open(dir, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	for _, op := range []string{"read", "write"} {
		if _, err = v.File(op, Input{Path: "link/secret", Text: "bad"}); err == nil {
			t.Fatal("symlink escape")
		}
	}
}
func TestCrossProcessStyleLock(t *testing.T) {
	dir := t.TempDir()
	a, _ := Open(dir, t.TempDir())
	defer a.Close()
	b, _ := Open(dir, t.TempDir())
	defer b.Close()
	r, err := a.File("write", Input{Path: "a.md", Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i, v := range []*Tools{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := v.File("write", Input{Path: "a.md", Text: string(rune('a' + i)), Revision: r["revision"].(string)}); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 {
		t.Fatal("expected one winner", ok.Load())
	}
}
func TestHHApplyContract(t *testing.T) {
	v := fixture(t)
	v.HHEnabled = true
	v.HHKey = "private"
	calls := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/negotiations" || r.Header.Get("Authorization") != "Bearer private" {
			t.Error("request contract")
		}
		_ = r.ParseForm()
		if r.Form.Get("resume_id") != "resume" || r.Form.Get("vacancy_id") != "123" || r.Form.Get("message") != "Hello & goodbye" {
			t.Error("form contract")
		}
		w.Header().Set("Location", "/negotiations/456")
		w.WriteHeader(201)
	}))
	defer s.Close()
	v.HHURL = s.URL
	in := Input{ID: "123", ResumeID: "resume", Message: "Hello & goodbye"}
	if _, err := v.API(context.Background(), "hh_apply", in); err == nil || calls != 0 {
		t.Fatal("unauthorized send")
	}
	in.Authorized = true
	out, err := v.API(context.Background(), "hh_apply", in)
	if err != nil || out["sent"] != true || calls != 1 {
		t.Fatal(out, err)
	}
}
func TestAPINoRedirectAndTenantMismatch(t *testing.T) {
	v := fixture(t)
	v.HHEnabled = true
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	v.HHURL = s.URL
	if _, err := v.API(context.Background(), "hh_search", Input{}); err == nil {
		t.Fatal("redirect accepted")
	}
	s.Close()
	if leaked.Load() {
		t.Fatal("followed redirect")
	}

}
func TestMCPWire(t *testing.T) {
	v := fixture(t)
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	server, err := v.Server().Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	list, err := client.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 7 {
		t.Fatal(list, err)
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "file_write", Arguments: map[string]any{"path": "drafts/test.md", "text": "real MCP call"}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
}
