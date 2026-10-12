package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTaskControlAPIIsOwnerScopedAndDoesNotSwitchCurrent(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "synthetic"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, owner string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer synthetic")
		}
		r.Header.Set("X-Hub-Principal", owner)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	if w := request("POST", "/v1/tasks", `{"name":"fixture"}`, "alice", false); w.Code != 401 {
		t.Fatal("unauthenticated creation", w.Code)
	}
	w := request("POST", "/v1/tasks", `{"name":"fixture"}`, "alice", true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var task Task
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.PrincipalID != "alice" || task.ChatID != 11 || task.TaskID == "default" {
		t.Fatal("wrong owner binding", task)
	}
	current, err := g.spool.ResolveTask(c.Users[0], 11, 0, false)
	if err != nil || current.TaskID == task.TaskID {
		t.Fatal("test task switched current", current, err)
	}
	if w := request("DELETE", "/v1/tasks/"+task.TaskID, "", "bob", true); w.Code == 200 {
		t.Fatal("foreign task deleted")
	}
	if w := request("DELETE", "/v1/tasks/"+task.TaskID, "", "alice", true); w.Code != 200 {
		t.Fatal("owner cleanup", w.Code)
	}
	for _, body := range []string{`{`, `{"name":""}`} {
		if w := request("POST", "/v1/tasks", body, "alice", true); w.Code != 400 {
			t.Fatal("invalid name accepted", w.Code)
		}
	}
	if w := request(http.MethodGet, "/v1/tasks", "", "alice", true); w.Code != 405 {
		t.Fatal("unexpected method", w.Code)
	}
}
