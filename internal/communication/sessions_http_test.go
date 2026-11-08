package communication

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionControlOwnerAndLifecycle(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}, StateDir: filepath.Join(t.TempDir(), "state"), WorkspaceDir: filepath.Join(t.TempDir(), "workspace")}
	if err := os.MkdirAll(bob.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bob.WorkspaceDir, 0700); err != nil {
		t.Fatal(err)
	}
	c.Users = append(c.Users, bob)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	aliceTask, _, err := g.spool.CreateTask(c.Users[0], 11, 0, "isolated alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bobTask, _, err := g.spool.CreateTask(bob, 22, 0, "isolated bob", false)
	if err != nil {
		t.Fatal(err)
	}
	call := func(owner string, input map[string]any) *httptest.ResponseRecorder {
		body, _ := json.Marshal(input)
		r := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+c.ControlAuth)
		r.Header.Set("X-Hub-Principal", owner)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("alice", map[string]any{"action": "create", "title": "new isolated"}); w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"created":true`)) {
		t.Fatalf("create = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "remove", "id": bobTask.TaskID}); w.Code != http.StatusNotFound {
		t.Fatalf("foreign remove = %d", w.Code)
	}
	if _, err := g.spool.TaskByID(bob, 22, bobTask.TaskID); err != nil {
		t.Fatal("foreign task changed", err)
	}
	if w := call("alice", map[string]any{"action": "switch", "id": aliceTask.TaskID}); w.Code != http.StatusOK {
		t.Fatalf("switch = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "edit", "edit_action": "archive", "id": aliceTask.TaskID}); w.Code != http.StatusOK {
		t.Fatalf("archive = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "switch", "id": aliceTask.TaskID}); w.Code != http.StatusConflict {
		t.Fatalf("archived switch = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "edit", "edit_action": "restore", "id": aliceTask.TaskID}); w.Code != http.StatusOK {
		t.Fatalf("restore = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "read", "id": aliceTask.TaskID}); w.Code != http.StatusOK {
		t.Fatalf("unstarted read = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "remove", "id": aliceTask.TaskID}); w.Code != http.StatusOK {
		t.Fatalf("remove = %d", w.Code)
	}
	if w := call("alice", map[string]any{"action": "read", "id": aliceTask.TaskID}); w.Code != http.StatusNotFound {
		t.Fatalf("removed read = %d", w.Code)
	}
}
