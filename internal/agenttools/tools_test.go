package agenttools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/media"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/modelcontextprotocol/go-sdk/mcp"
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

func TestSelfEnvUpdateSuggestsNextStepForJira(t *testing.T) {
	t.Setenv("HUB_STATE", t.TempDir())
	t.Setenv("HUB_SELF_ENV_KEYS", "JIRA_API_TOKEN,JIRA_URL,JIRA_USERNAME")
	v := fixture(t)
	v.Restart = func() error { return nil }
	out, err := v.EnvUpdate(Input{Text: "JIRA_URL=https://jira.example\nJIRA_USERNAME=owner@example.com\nJIRA_API_TOKEN=token"})
	if err != nil || !strings.Contains(out["next_step"].(string), "Jira") {
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
	t.Setenv("JIRA_URL", "")
	t.Setenv("JIRA_USERNAME", "")
	t.Setenv("JIRA_API_TOKEN", "")
	out, err = v.ServiceEnable(Input{Service: "atlassian"})
	if err != nil || out["enabled"] != false {
		t.Fatal(out, err)
	}
	missing := out["missing_env"].([]string)
	if len(missing) != 3 || missing[0] != "JIRA_API_TOKEN" || missing[1] != "JIRA_URL" || missing[2] != "JIRA_USERNAME" {
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

func TestToolHubManifestCatalogEnableDisableIsOptIn(t *testing.T) {
	state := t.TempDir()
	storePath := filepath.Join(state, "toolhub", "store.json")
	store := toolhub.NewStore()
	grant := toolhub.OperatorGrant(toolhub.GrantCatalogDefault, "alice", "", "")
	if err := toolhub.Confirm(&grant, "", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	definition := toolhub.ToolDefinition{
		Schema: toolhub.SchemaVersion, DefinitionID: "demo", Version: "1.0.0", Transport: toolhub.RemoteMCP,
		Source:    toolhub.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"},
		Tools:     []toolhub.ToolSpec{{Name: "search", Effect: toolhub.ReadEffect}},
		Workload:  toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "owner-scoped test"},
		Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}},
		Health:    toolhub.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	binding, err := store.Enable(auth, definition.DefinitionID, definition.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", state)
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	v, err := Open(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	out, err := v.ServiceCatalog()
	if err != nil || out["source"] != "toolhub-manifest" || out["secret_values_included"] != false {
		t.Fatal(out, err)
	}
	if disabled, err := v.ServiceDisable(Input{Service: "demo@1.0.0"}); err != nil || disabled["enabled"] != false {
		t.Fatal(disabled, err)
	}
	enabled, err := v.ServiceEnable(Input{Service: "demo"})
	if err != nil || enabled["enabled"] != true || enabled["binding_id"] != binding.ToolBindingID {
		t.Fatal(enabled, err)
	}
	missingDefinition := toolhub.ToolDefinition{
		Schema: toolhub.SchemaVersion, DefinitionID: "needs", Version: "1.0.0", Transport: toolhub.RemoteMCP,
		Source: toolhub.DefinitionSource{URL: "https://needs.example/mcp", TLSMode: "required"}, Tools: []toolhub.ToolSpec{{Name: "read", Effect: toolhub.ReadEffect}},
		Credentials: []toolhub.CredentialInput{{Name: "NEEDS_TOKEN", Required: true}}, Workload: toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "credential test"},
		Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"needs.example"}}, Health: toolhub.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := v.ToolHub.RegisterDefinition(missingDefinition); err != nil {
		t.Fatal(err)
	}
	if err := v.ToolHub.Save(storePath); err != nil {
		t.Fatal(err)
	}
	missing, err := v.ServiceEnable(Input{Service: "needs"})
	if err != nil || missing["enabled"] != false || len(missing["missing_credentials"].([]string)) != 1 {
		t.Fatal(missing, err)
	}
	if _, err := v.ServiceEnable(Input{Service: "unknown"}); err == nil {
		t.Fatal("unknown ToolHub manifest accepted")
	}
	t.Setenv("HUB_TOOLHUB_STORE", "")
	legacy := fixture(t)
	if _, err := legacy.ServiceDisable(Input{Service: "demo"}); err == nil {
		t.Fatal("legacy service_disable accepted")
	}
	if _, err := os.Stat(filepath.Join(state, toolhub.ReconnectMarkerName)); err != nil {
		t.Fatal("reconnect marker missing after catalog mutation:", err)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := reloaded.ListProjectedTools(auth)
	if err != nil || len(listed) == 0 {
		t.Fatalf("persisted enable missing after reload: %+v err=%v", listed, err)
	}
}

func TestToolHubOpenRejectsInvalidIdentityAndOrganizationMutation(t *testing.T) {
	state := t.TempDir()
	storePath := filepath.Join(state, "store.json")
	if err := toolhub.NewStore().Save(storePath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", state)
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_PRINCIPAL_ID", "")
	t.Setenv("HUB_USER_ID", "")
	if _, err := Open(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("invalid ToolHub identity accepted")
	}
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	v, err := Open(t.TempDir(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.ServiceEnable(Input{Service: "missing"}); err == nil {
		t.Fatal("organization ToolHub enable accepted")
	}
}

func TestToolHubOrganizationCanDisableButNotEnable(t *testing.T) {
	state := t.TempDir()
	storePath := filepath.Join(state, "toolhub", "store.json")
	store := toolhub.NewStore()
	grant := toolhub.OperatorGrant(toolhub.GrantDefinition, "alice", "demo", "1.0.0")
	if err := toolhub.Confirm(&grant, "", time.Unix(1, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutGrant(grant); err != nil {
		t.Fatal(err)
	}
	definition := toolhub.ToolDefinition{
		Schema: toolhub.SchemaVersion, DefinitionID: "demo", Version: "1.0.0", Transport: toolhub.RemoteMCP,
		Source:    toolhub.DefinitionSource{URL: "https://example.invalid/mcp", TLSMode: "required"},
		Tools:     []toolhub.ToolSpec{{Name: "search", Effect: toolhub.ReadEffect}},
		Workload:  toolhub.WorkloadPolicy{Class: toolhub.PerUser, Rationale: "owner-scoped test"},
		Execution: toolhub.ExecutionPolicy{TimeoutSeconds: 30, OutputBytes: 1 << 20, CPUMillis: 500, MemoryMiB: 256, MaxPIDs: 32, Egress: []string{"example.invalid"}},
		Health:    toolhub.HealthProbe{Kind: "http", Value: "/health", TimeoutSeconds: 5},
	}
	if err := store.RegisterDefinition(definition); err != nil {
		t.Fatal(err)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: "alice", ExternalIdentityID: "alice", ContextID: "alice", RuntimeID: "alice", ConversationID: "test", DeliveryTargetID: "test", PolicyVersion: "policy-1"}
	if _, err := store.Enable(auth, definition.DefinitionID, definition.Version); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(storePath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_STATE", state)
	t.Setenv("HUB_TOOLHUB_STORE", storePath)
	t.Setenv("HUB_PRINCIPAL_ID", "alice")
	t.Setenv("HUB_CONTEXT_ID", "alice")
	t.Setenv("HUB_RUNTIME_ID", "alice")
	v, err := Open(t.TempDir(), t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.ServiceEnable(Input{Service: "demo"}); err == nil {
		t.Fatal("organization ToolHub enable accepted")
	}
	disabled, err := v.ServiceDisable(Input{Service: "demo@1.0.0"})
	if err != nil || disabled["enabled"] != false {
		t.Fatalf("organization ToolHub disable rejected: %+v err=%v", disabled, err)
	}
	reloaded, err := toolhub.Load(storePath)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := reloaded.ListProjectedTools(auth)
	if err != nil || len(listed) != 0 {
		t.Fatalf("org disable did not persist: %+v err=%v", listed, err)
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
	t.Setenv("HUB_HH_ENABLED", "false")
	t.Setenv("HUB_HERMES_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
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
	if err != nil || len(list.Tools) != 21 {
		t.Fatal(len(list.Tools), err)
	}
	for _, name := range []string{"document_extract", "document_create", "image_inspect", "artifact_remove", "settings_get", "settings_set"} {
		found := false
		for _, tool := range list.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Fatal("missing", name)
		}
	}
	for _, tool := range list.Tools {
		if tool.Name == "image_generate" || tool.Name == "image_edit" {
			t.Fatal("image generation published without a grant")
		}
	}
	for _, tool := range list.Tools {
		if tool.Name == "env_update" {
			t.Fatal("legacy chat credential tool is still published")
		}
	}
	listed, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "routine_list", Arguments: map[string]any{}})
	if err == nil && listed != nil && !listed.IsError {
		t.Fatal("routine_list without communication hub succeeded")
	}
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "file_write", Arguments: map[string]any{"path": "drafts/test.md", "text": "real MCP call"}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
}

func TestMediaThroughHubMCP(t *testing.T) {
	t.Setenv("HUB_HH_ENABLED", "false")
	v := fixture(t)
	pngBody := hubPNG(t)
	var sawInspect, sawGenerate atomic.Bool
	var imageURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			sawInspect.Store(true)
			if r.Header.Get("Authorization") != "Bearer openai-secret-value" || strings.Contains(r.Header.Get("Authorization"), "fal-secret-value") {
				t.Errorf("inspect auth %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"a red pixel"}}]}`))
		case "/" + media.ImageModel:
			sawGenerate.Store(true)
			if r.Header.Get("Authorization") != "Key fal-secret-value" || strings.Contains(r.Header.Get("Authorization"), "openai-secret-value") {
				t.Errorf("generate auth %q", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("payload: %v", err)
			}
			if _, extra := payload["num_images"]; extra || payload["image_size"] != media.FalImageSize || payload["output_format"] != media.FalFormat {
				t.Errorf("payload %#v", payload)
			}
			_, _ = w.Write([]byte(`{"images":[{"url":"` + imageURL + `"}]}`))
		case "/img.png":
			_, _ = w.Write(pngBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	imageURL = server.URL + "/img.png"
	v.session().FalBase = server.URL
	t.Setenv("OPENAI_API_KEY", "openai-secret-value")
	t.Setenv("FAL_KEY", "fal-secret-value")
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	writeGrant := func(granted bool) {
		t.Helper()
		body := "model:\n  default: fixture-model\n  base_url: \"" + server.URL + "/v1\"\n"
		if granted {
			body += "image_gen:\n  provider: fal\n  model: " + media.ImageModel + "\n"
		}
		if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeGrant(false)
	t.Setenv("HUB_HERMES_CONFIG", cfg)
	root := v.Workspace.Name()
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dot.png"), pngBody, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	client, cleanup := connectHub(t, v)
	defer cleanup()
	list, err := client.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 21 {
		t.Fatal(len(list.Tools), err)
	}
	for _, tool := range list.Tools {
		if tool.Name == "image_generate" || tool.Name == "image_edit" {
			t.Fatal("image generation published without a grant")
		}
		if tool.Name == "document_extract" && !strings.Contains(tool.Description, "docx") {
			t.Fatal(tool.Description)
		}
	}
	missing, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "image_generate", Arguments: map[string]any{"name": "pic", "prompt": "cat"}})
	if err == nil && (missing == nil || !missing.IsError) {
		t.Fatal("generated without a grant", err, missing)
	}

	extracted, body := callHub(t, client, "document_extract", map[string]any{"path": "notes.md"})
	if extracted.IsError || body["text"] != "alpha beta" || body["format"] != "md" || body["secret_values_included"] != false {
		t.Fatal(extracted, body)
	}
	created, body := callHub(t, client, "document_create", map[string]any{"name": "report", "format": "txt", "text": "created body"})
	if created.IsError || body["path"] != "artifacts/documents/report.txt" {
		t.Fatal(created, body)
	}
	reread, body := callHub(t, client, "document_extract", map[string]any{"path": "artifacts/documents/report.txt"})
	if reread.IsError || body["text"] != "created body" {
		t.Fatal(reread, body)
	}
	edited, body := callHub(t, client, "document_edit", map[string]any{"path": "artifacts/documents/report.txt", "text": "edited body"})
	if edited.IsError || body["path"] != "artifacts/documents/report.txt" {
		t.Fatal(edited, body)
	}
	reread, body = callHub(t, client, "document_extract", map[string]any{"path": "artifacts/documents/report.txt"})
	if reread.IsError || body["text"] != "edited body" {
		t.Fatal(reread, body)
	}
	converted, body := callHub(t, client, "document_convert", map[string]any{"path": "notes.md", "name": "notes", "format": "html"})
	if converted.IsError || body["path"] != "artifacts/documents/notes.html" {
		t.Fatal(converted, body)
	}
	shot, body := callHub(t, client, "image_convert", map[string]any{"path": "dot.png", "name": "shot", "format": "jpeg"})
	if shot.IsError || body["path"] != "artifacts/images/shot.jpg" {
		t.Fatal(shot, body)
	}
	seen, body := callHub(t, client, "image_inspect", map[string]any{"path": "dot.png"})
	if seen.IsError || body["text"] != "a red pixel" || jsonNumber(body["width"]) != 2 || jsonNumber(body["height"]) != 3 || !sawInspect.Load() {
		t.Fatal(seen, body)
	}
	for _, path := range []string{"../notes.md", filepath.Join(root, "notes.md"), `a\b.md`} {
		denied, _ := callHub(t, client, "document_extract", map[string]any{"path": path})
		if !denied.IsError {
			t.Fatal("accepted", path)
		}
	}
	removed, body := callHub(t, client, "artifact_remove", map[string]any{"path": "artifacts/documents/report.txt"})
	if removed.IsError || body["removed"] != true {
		t.Fatal(removed, body)
	}
	gone, _ := callHub(t, client, "document_extract", map[string]any{"path": "artifacts/documents/report.txt"})
	if !gone.IsError {
		t.Fatal("removed artifact still readable")
	}
	kept, err := os.ReadFile(filepath.Join(root, "notes.md"))
	if err != nil || string(kept) != "alpha beta" {
		t.Fatal("source document changed", err)
	}

	writeGrant(true)
	granted, cleanupGranted := connectHub(t, v)
	defer cleanupGranted()
	listed, err := granted.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 25 {
		t.Fatal(len(listed.Tools), err)
	}
	made, body := callHub(t, granted, "image_generate", map[string]any{"name": "pic", "prompt": "a red square"})
	if made.IsError || body["path"] != "artifacts/images/pic.png" || body["model"] != media.ImageModel || !sawGenerate.Load() {
		t.Fatal(made, body)
	}
	wire, err := json.Marshal(made)
	if err != nil || strings.Contains(string(wire), imageURL) || strings.Contains(string(wire), "fal.run") {
		t.Fatal("provider URL returned", err, string(wire))
	}
	stored, err := os.ReadFile(filepath.Join(root, "artifacts", "images", "pic.png"))
	if err != nil || !bytes.Equal(stored, pngBody) || bytes.Contains(stored, []byte("fal-secret-value")) {
		t.Fatal(err)
	}
	// Async media tools are registered under the same grant; without a
	// configured media service they surface a clean error, not a panic.
	submitted, _ := callHub(t, granted, "video_generate", map[string]any{"prompt": "a rocket"})
	if !submitted.IsError {
		t.Fatal("video submit without service")
	}
	fetched, _ := callHub(t, granted, "media_fetch", map[string]any{"id": "job-1", "name": "clip"})
	if !fetched.IsError {
		t.Fatal("media fetch without service")
	}
	writeGrant(false)
	revoked, _ := callHub(t, granted, "image_generate", map[string]any{"name": "later", "prompt": "cat"})
	if !revoked.IsError || !strings.Contains(toolPlain(revoked), "grant") {
		t.Fatal(toolPlain(revoked))
	}
}

func connectHub(t *testing.T, v *Tools) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	left, right := mcp.NewInMemoryTransports()
	server, err := v.Server().Connect(ctx, left, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, right, nil)
	if err != nil {
		_ = server.Close()
		t.Fatal(err)
	}
	return client, func() {
		_ = client.Close()
		_ = server.Close()
	}
}

func callHub(t *testing.T, client *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
	t.Helper()
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(name, err)
	}
	if result == nil {
		t.Fatal(name, "nil result")
	}
	plain := toolPlain(result)
	for _, secret := range []string{"openai-secret-value", "fal-secret-value"} {
		if strings.Contains(plain, secret) {
			t.Fatalf("%s result included a credential", name)
		}
	}
	if result.StructuredContent == nil {
		return result, nil
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(string(encoded), err)
	}
	return result, body
}

func toolPlain(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return string(raw)
}

func jsonNumber(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case json.Number:
		parsed, _ := n.Float64()
		return parsed
	default:
		return -1
	}
}

func hubPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 3))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestToolHubEnvOr(t *testing.T) {
	t.Setenv("X_ENV_PRESENT", "v")
	if toolHubEnvOr("X_ENV_PRESENT", "fb") != "v" || toolHubEnvOr("X_ENV_ABSENT", "fb") != "fb" {
		t.Fatal("toolHubEnvOr")
	}
}
