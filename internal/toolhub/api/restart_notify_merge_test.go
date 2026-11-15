package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRuntimeRestartNotifierContract(t *testing.T) {
	var posts []map[string]any
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
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("restart request: %v", err)
		}
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

type failingRestartState struct{}

func (failingRestartState) AppliedRestartRevision(ProjectionTarget) uint64 { return 0 }
func (failingRestartState) MarkRestartApplied(ProjectionTarget) error {
	return errors.New("fixture write failed")
}

func TestRestartNotifierReportsAppliedWriteFailure(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	n := &RuntimeRestartNotifier{URL: server.URL, Token: "fixture", State: failingRestartState{}}
	if err := n.Notify([]ProjectionTarget{{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 2}}); err == nil || posts != 1 {
		t.Fatalf("ack write failure hidden: posts=%d err=%v", posts, err)
	}
}

type recordingRestartState struct{ applied uint64 }

func (s *recordingRestartState) AppliedRestartRevision(ProjectionTarget) uint64 { return s.applied }
func (s *recordingRestartState) MarkRestartApplied(target ProjectionTarget) error {
	s.applied = target.Revision
	return nil
}

func TestProjectionBurstAppliesLatestOnce(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	state := &recordingRestartState{}
	n := &RuntimeRestartNotifier{URL: server.URL, Token: "fixture", State: state}
	target := ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 2}
	latest := target
	latest.Revision = 3
	if err := n.Notify([]ProjectionTarget{target, latest}); err != nil || posts != 1 || state.applied != 3 {
		t.Fatalf("burst posts=%d applied=%d err=%v", posts, state.applied, err)
	}
}

func TestProjectionBurstBaselineKeepsLatest(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	n := &RuntimeRestartNotifier{URL: server.URL, Token: "fixture"}
	latest := ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 3}
	old := latest
	old.Revision = 2
	if err := n.Notify([]ProjectionTarget{latest, old}); err != nil {
		t.Fatal(err)
	}
	if err := n.Notify([]ProjectionTarget{latest}); err != nil || posts != 0 {
		t.Fatalf("baseline regressed: posts=%d err=%v", posts, err)
	}
}

func TestRestartNotifierCarriesStableOperation(t *testing.T) {
	var requests []struct {
		Revision    uint64 `json:"revision"`
		OperationID string `json:"operation_id"`
		Reason      string `json:"reason"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Revision    uint64 `json:"revision"`
			OperationID string `json:"operation_id"`
			Reason      string `json:"reason"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("request: %v", err)
		}
		requests = append(requests, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	n := &RuntimeRestartNotifier{URL: server.URL, Token: "fixture", State: failingRestartState{}}
	target := ProjectionTarget{PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", Revision: 2, Digest: "sha256:synthetic"}
	_ = n.Notify([]ProjectionTarget{target})
	_ = n.Notify([]ProjectionTarget{target})
	target.Revision++
	_ = n.Notify([]ProjectionTarget{target})
	if len(requests) != 3 || requests[0].Revision != 2 || requests[0].Reason != "tool_projection_change" || requests[0].OperationID == "" || requests[0] != requests[1] || requests[2].Revision != 3 || requests[2].OperationID == requests[0].OperationID {
		t.Fatalf("unstable operation: %+v", requests)
	}
}
