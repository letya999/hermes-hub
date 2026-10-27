package agenttools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAssignmentToolsMapOps (SPEC-0080 R2.3): every assignment op maps to its
// REST route/method — nothing reaches a different surface.
func TestAssignmentToolsMapOps(t *testing.T) {
	want := map[string][2]string{
		"assignment_plan":     {http.MethodPost, "/v1/assignments/plan"},
		"assignment_create":   {http.MethodPost, "/v1/assignments"},
		"assignment_list":     {http.MethodGet, "/v1/assignments"},
		"assignment_get":      {http.MethodGet, "/v1/assignments/asg-1"},
		"assignment_update":   {http.MethodPost, "/v1/assignments/asg-1/plan"},
		"assignment_apply":    {http.MethodPost, "/v1/assignments/asg-1/apply"},
		"assignment_pause":    {http.MethodPost, "/v1/assignments/asg-1/pause"},
		"assignment_resume":   {http.MethodPost, "/v1/assignments/asg-1/resume"},
		"assignment_cancel":   {http.MethodPost, "/v1/assignments/asg-1/cancel"},
		"assignment_complete": {http.MethodPost, "/v1/assignments/asg-1/complete"},
	}
	for op, want := range want {
		t.Run(op, func(t *testing.T) {
			var gotMethod, gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				_, _ = io.WriteString(w, `{"assignment_id":"asg-1","state":"active"}`)
			}))
			defer server.Close()
			v := fixture(t)
			v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
			in := Input{ID: "asg-1", ExpectedRevision: 3, Text: "watch x", Plan: `{"revision":1,"sources":[]}`}
			if _, err := v.Assignment(context.Background(), op, in); err != nil {
				t.Fatalf("%s failed: %v", op, err)
			}
			if gotMethod != want[0] || gotPath != want[1] {
				t.Fatalf("%s mapped to %s %s, want %s %s", op, gotMethod, gotPath, want[0], want[1])
			}
		})
	}
}

// TestAssignmentToolRequiresRevision (SPEC-0080 R2.3): mutations on an
// existing assignment need expected_revision — the CAS fence.
func TestAssignmentToolRequiresRevision(t *testing.T) {
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = "http://127.0.0.1:1", "token"
	for _, op := range []string{"assignment_update", "assignment_pause", "assignment_resume", "assignment_cancel", "assignment_complete", "assignment_apply"} {
		if _, err := v.Assignment(context.Background(), op, Input{ID: "asg-1"}); err == nil || !strings.Contains(err.Error(), "expected_revision") {
			t.Fatalf("%s must require expected_revision, got %v", op, err)
		}
	}
}

// TestAssignmentToolPayloads (SPEC-0080 R2.3): create sends intent/title/
// plan/apply; update sends plan + expected_revision for the CAS fence.
func TestAssignmentToolPayloads(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"

	if _, err := v.Assignment(context.Background(), "assignment_create", Input{Text: "watch x", Title: "prices", Plan: `{"revision":1,"sources":[]}`, Queue: true}); err != nil {
		t.Fatal(err)
	}
	var create map[string]any
	if err := json.Unmarshal(gotBody, &create); err != nil {
		t.Fatal(err)
	}
	if create["intent"] != "watch x" || create["title"] != "prices" || create["apply"] != true || create["plan"] == nil {
		t.Fatalf("bad create payload: %s", gotBody)
	}
	if _, err := v.Assignment(context.Background(), "assignment_update", Input{ID: "asg-1", ExpectedRevision: 7, Plan: `{"revision":2,"sources":[]}`}); err != nil {
		t.Fatal(err)
	}
	var upd map[string]any
	if err := json.Unmarshal(gotBody, &upd); err != nil {
		t.Fatal(err)
	}
	if upd["expected_revision"] != float64(7) || upd["plan"] == nil {
		t.Fatalf("bad update payload: %s", gotBody)
	}
}

// TestAssignmentToolNoAuthorize (SPEC-0080 R2.4): no agent tool widens
// grants — authorize is a chat-only verb reachable by the owner alone.
func TestAssignmentToolNoAuthorize(t *testing.T) {
	found := false
	for _, tool := range HubToolsDefinition().Tools {
		if !strings.HasPrefix(tool.Name, "assignment_") {
			continue
		}
		found = true
		for _, arg := range tool.Arguments {
			if arg.Name == "authorize" {
				t.Fatalf("%s must not expose authorize", tool.Name)
			}
		}
	}
	if !found {
		t.Fatal("assignment tools not published")
	}
}

// TestAssignmentDispatchPublishedOps (SPEC-0080 R2.3): every published
// assignment_* tool actually dispatches — the catalog must not advertise
// verbs the executor drops.
func TestAssignmentDispatchPublishedOps(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"assignment_id":"a1"}`)
	}))
	defer server.Close()
	v := fixture(t)
	v.CommunicationURL, v.CommunicationAuth = server.URL, "token"
	for _, tool := range HubToolsDefinition().Tools {
		if !strings.HasPrefix(tool.Name, "assignment_") {
			continue
		}
		_, err := v.call(context.Background(), tool.Name, Input{ID: "a1", ExpectedRevision: 1, Plan: `{"revision":1,"sources":[]}`}, nil, nil, nil)
		if err != nil {
			t.Fatalf("%s published but not dispatched: %v", tool.Name, err)
		}
	}
}
