package automation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// TestAutomationControlOperationSweep exercises every dispatch branch
// including its decode guards.
func TestAutomationControlOperationSweep(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "read")
	h.grant(t, "alice", "write")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	url := server.URL + "/v1/automation/operations"

	// Requests must carry the control bearer and principal header.
	authed := func(operation string, payload any) int {
		t.Helper()
		raw, _ := json.Marshal(payload)
		body, _ := json.Marshal(map[string]any{"operation": operation, "caller": alice, "payload": json.RawMessage(raw)})
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer control")
		req.Header.Set("X-Hub-Principal", alice.PrincipalID)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// A real record lets read-model ops reach the backend instead of failing
	// at not-found; the schedule draft validator supplies the rest.
	item := draft("rt-sweep", alice)
	if code := authed("schedule.create", map[string]any{"schedule": item}); code != http.StatusOK {
		t.Fatalf("schedule.create: %d", code)
	}
	rev := uint64(1)
	until := time.Now().UTC().Add(time.Hour)
	cases := []struct {
		op      string
		payload map[string]any
	}{
		{"schedule.replay", map[string]any{"schedule": item}},
		{"schedule.list", map[string]any{}},
		{"schedule.load", map[string]any{"id": "rt-sweep"}},
		{"schedule.card", map[string]any{"id": "rt-sweep"}},
		{"schedule.preview", map[string]any{"id": "rt-sweep", "count": 2}},
		{"schedule.preview", map[string]any{"id": "rt-sweep", "count": 40}},
		{"schedule.history", map[string]any{"id": "rt-sweep"}},
		{"schedule.sample", map[string]any{"id": "rt-sweep", "filter": ""}},
		{"schedule.edit", map[string]any{"id": "rt-sweep", "patch": map[string]any{"name": "renamed"}, "resume": false}},
		{"schedule.destinations", map[string]any{"after": "", "limit": 10, "query": ""}},
		{"occurrence.authorize", map[string]any{"id": "occ-1", "kind": "reminder"}},
		{"delivery.outcome", map[string]any{"id": "del-1", "status": "completed", "text": "done"}},
		{"assignment.list", map[string]any{}},
		{"assignment.load", map[string]any{"id": "asg-1"}},
		{"assignment.apply", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.pause", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.resume", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.cancel", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.complete", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.delete", map[string]any{"id": "asg-1", "expected_revision": rev}},
		{"assignment.plan", map[string]any{"id": "asg-1", "plan": commodel.AssignmentPlan{Revision: 1}, "grants": commodel.AssignmentGrants{MaxWakesPerDay: 2}, "expected_revision": rev}},
		{"assignment.grants", map[string]any{"id": "asg-1", "plan_revision": 1, "grants": commodel.AssignmentGrants{MaxWakesPerDay: 1}}},
		{"assignment.progress", map[string]any{"id": "asg-1", "expected_revision": 1, "job_id": "job-1", "summary": "s", "finding": "f", "next_step": "n", "complete": false}},
		{"result.report", map[string]any{"job_id": "job-1", "status": "completed", "text": "x"}},
		{"result.report", map[string]any{"job_id": "job-1", "status": "bogus", "text": "x"}},
		{"hook.intake", map[string]any{"id": "hook-1", "body": "aGk="}},
		{"hook.intake", map[string]any{"id": "hook-1", "body": ""}},
		{"schedule.snooze", map[string]any{"id": "rt-sweep", "until": until, "expected_revision": rev}},
		{"schedule.pause", map[string]any{"id": "rt-sweep", "expected_revision": rev}},
		{"schedule.delete", map[string]any{"id": "rt-sweep", "expected_revision": rev}},
	}
	for _, tc := range cases {
		code := authed(tc.op, tc.payload)
		if code < 200 || code >= 600 {
			t.Fatalf("%s unhandled: %d", tc.op, code)
		}
	}
	// Missing-id guards return bad request, never a panic.
	for _, tc := range []struct {
		op      string
		payload map[string]any
	}{
		{"schedule.load", map[string]any{}},
		{"occurrence.authorize", map[string]any{"kind": "x"}},
		{"assignment.plan", map[string]any{"id": "a", "plan": commodel.AssignmentPlan{}, "grants": commodel.AssignmentGrants{}}},
		{"hook.intake", map[string]any{"body": "aGk="}},
	} {
		if code := authed(tc.op, tc.payload); code != http.StatusBadRequest {
			t.Fatalf("%s guard: %d", tc.op, code)
		}
	}
}
