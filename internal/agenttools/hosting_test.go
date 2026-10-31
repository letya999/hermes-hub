package agenttools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/hosting"
)

func TestSiteToolsBoundToPrivateService(t *testing.T) {
	t.Setenv("HUB_HOSTING_URL", "")
	t.Setenv("HUB_HOSTING_TOKEN", "")
	t.Setenv("HUB_HOSTING_TOKEN_FILE", "")
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "allowed"), 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	sub, err := workspace.OpenRoot("allowed")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	token := strings.Repeat("a", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("wrong credential")
		}
		var in hosting.PublishRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Source != "allowed/page" || in.Name != "page" {
			t.Error("source not bound to admitted workspace")
		}
		_ = json.NewEncoder(w).Encode(hosting.Site{Name: "page", State: "ready", Revision: 1})
	}))
	defer server.Close()
	tools := &Tools{Workspace: workspace, HTTP: server.Client()}
	if _, err := tools.hostingCall(context.Background(), "site_list", Input{}, workspace); err == nil || calls != 0 {
		t.Fatal("feature-off dispatch reached service")
	}
	t.Setenv("HUB_HOSTING_URL", server.URL)
	t.Setenv("HUB_HOSTING_TOKEN", token)
	if _, err = tools.hostingCall(context.Background(), "site_publish", Input{Name: "page", Path: "page", RequestID: "create", Access: "public"}, sub); err != hosting.ErrDenied || calls != 0 {
		t.Fatal("implicit public publication accepted")
	}
	for _, p := range []string{"../outside", "page/../../outside", "/absolute", `C:\foreign`, `page\..\outside`} {
		if _, err := tools.hostingCall(context.Background(), "site_publish", Input{Name: "page", Path: p, RequestID: "escape"}, sub); err != hosting.ErrDenied || calls != 0 {
			t.Fatal("source escaped delegated workspace")
		}
	}
	result, err := tools.hostingCall(context.Background(), "site_publish", Input{Name: "page", Path: "page", RequestID: "create"}, sub)
	if err != nil || result == nil || calls != 1 {
		t.Fatalf("own publish: %v", err)
	}
	foreign, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if _, err = tools.hostingCall(context.Background(), "site_publish", Input{Name: "page", Path: "page"}, foreign); err != hosting.ErrDenied || calls != 1 {
		t.Fatal("foreign workspace reached hosting")
	}
}

func TestSiteToolsPublishListStatusUnpublish(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(workspacePath, "page"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspacePath, "page", "index.html"), []byte("private fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	service, err := hosting.Open(hosting.Config{Owner: "alice", Environment: "dev", Zone: "alice", Domain: "hub.example.test", Workspace: workspacePath, Store: filepath.Join(root, "sites")}, hosting.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	token := strings.Repeat("a", 32)
	server := httptest.NewServer(service.ControlHandler(token))
	defer server.Close()
	workspace, err := os.OpenRoot(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	tools := &Tools{Workspace: workspace, HTTP: server.Client()}
	t.Setenv("HUB_HOSTING_URL", server.URL)
	t.Setenv("HUB_HOSTING_TOKEN", token)
	t.Setenv("HUB_HOSTING_TOKEN_FILE", "")
	out, err := tools.Call(context.Background(), "site_publish", Input{Name: "page", Path: "page", Mode: "static", Access: "private", RequestID: "create"})
	if err != nil {
		t.Fatal(err)
	}
	published, ok := out.(map[string]any)
	if !ok || published["revision"] != float64(1) || published["url"] != nil {
		t.Fatal("private publication returned invalid metadata")
	}
	out, err = tools.Call(context.Background(), "site_list", Input{})
	if err != nil {
		t.Fatal(err)
	}
	if list, ok := out.([]any); !ok || len(list) != 1 {
		t.Fatal("list lost own slot")
	}
	out, err = tools.Call(context.Background(), "site_status", Input{Name: "page"})
	if err != nil {
		t.Fatal(err)
	}
	if status := out.(map[string]any); status["state"] != "ready" || status["token_hash"] != nil {
		t.Fatal("unsafe status")
	}
	if _, err = tools.Call(context.Background(), "site_unpublish", Input{Name: "page", ExpectedRevision: 1, RequestID: "remove"}); err != nil {
		t.Fatal(err)
	}
	out, err = tools.Call(context.Background(), "site_list", Input{})
	if err != nil || len(out.([]any)) != 0 {
		t.Fatal("unpublish did not revoke own slot")
	}
}
