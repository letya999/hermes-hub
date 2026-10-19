package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeRestartNotifierContract(t *testing.T) {
	var posts []map[string]string
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/restart-request" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(400)
			return
		}
		if fail {
			w.WriteHeader(500)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		posts = append(posts, body)
		w.WriteHeader(200)
	}))
	defer server.Close()

	n := &RuntimeRestartNotifier{URL: server.URL, Token: "tok", HTTP: server.Client()}
	targets := []ProjectionTarget{
		{PrincipalID: "p1", ContextID: "c1", RuntimeID: "r1", Revision: 3},
		{PrincipalID: "p2", ContextID: "c1", RuntimeID: "r1", Revision: 1},
	}
	// first snapshot is adopted as the baseline — nothing is posted
	if err := n.Notify(targets); err != nil || len(posts) != 0 {
		t.Fatalf("baseline posted: %v %v", posts, err)
	}
	// only bumped revisions trigger a restart request
	targets[0].Revision = 4
	if err := n.Notify(targets); err != nil || len(posts) != 1 || posts[0]["principal_id"] != "p1" {
		t.Fatalf("bump: %v %v", posts, err)
	}
	// same revision is not resent
	if err := n.Notify(targets); err != nil || len(posts) != 1 {
		t.Fatalf("resent: %v", posts)
	}
	// a failed post is returned and retried on the next tick
	fail = true
	targets[1].Revision = 2
	if err := n.Notify(targets); err == nil || !strings.Contains(err.Error(), "p2") {
		t.Fatalf("failure: %v", err)
	}
	fail = false
	if err := n.Notify(targets); err != nil || len(posts) != 2 || posts[1]["principal_id"] != "p2" {
		t.Fatalf("retry: %v %v", posts, err)
	}
}

func TestRuntimeRestartNotifierFromEnv(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	t.Setenv("HUB_COMMUNICATION_AUTH", "")
	if RuntimeRestartNotifierFromEnv() != nil {
		t.Fatal("notifier without configuration")
	}
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "http://127.0.0.1:1/")
	t.Setenv("HUB_COMMUNICATION_AUTH", " tok ")
	n := RuntimeRestartNotifierFromEnv()
	if n == nil || n.URL != "http://127.0.0.1:1" || n.Token != "tok" {
		t.Fatalf("notifier: %+v", n)
	}
}
