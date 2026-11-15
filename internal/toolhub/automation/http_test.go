package automation

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
)

func TestAutomationControlHTTPPinsPrincipalAndRights(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.envelope(t, "alice"), h.envelope(t, "bob")
	h.grant(t, "alice", "read")
	h.grant(t, "alice", "write")
	h.grant(t, "bob", "read")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	call := func(token, principal, operation string, caller identity.Envelope, payload any, origin string) (int, []byte) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"operation": operation, "caller": caller, "payload": payload})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/v1/automation/operations", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Hub-Principal", principal)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result bytes.Buffer
		if _, err := result.ReadFrom(resp.Body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, result.Bytes()
	}
	item := draft("rt-http", alice)
	client := ControlClient{URL: server.URL, Token: "control"}
	var created commodel.Schedule
	if err := client.Call(context.Background(), alice, "schedule.create", map[string]any{"schedule": item}, &created); err != nil || created.ScheduleID != item.ScheduleID {
		t.Fatalf("control client create: %+v, %v", created, err)
	}
	var list []commodel.Schedule
	if err := client.Call(context.Background(), alice, "schedule.list", nil, &list); err != nil || len(list) != 1 {
		t.Fatalf("control client list: %+v, %v", list, err)
	}
	if code, body := call("control", "alice", "schedule.pause", alice, map[string]any{"id": created.ScheduleID, "expected_revision": created.Revision + 1}, ""); code != http.StatusConflict {
		t.Fatalf("stale pause revision: %d %s", code, body)
	}
	if code, body := call("control", "alice", "schedule.pause", alice, map[string]any{"id": created.ScheduleID, "expected_revision": created.Revision}, ""); code != http.StatusOK {
		t.Fatalf("current pause revision: %d %s", code, body)
	}
	list = nil
	if err := client.Call(context.Background(), alice, "schedule.list", nil, &list); err != nil || len(list) != 1 || !list[0].Paused {
		t.Fatalf("paused schedule list: %+v, %v", list, err)
	}
	assignment := commodel.Assignment{
		AssignmentID: "asg-http", Envelope: alice, Intent: "track progress", Title: "progress", State: "draft",
	}
	if err := client.Call(context.Background(), alice, "assignment.create", map[string]any{"assignment": assignment}, &assignment); err != nil {
		t.Fatalf("assignment create: %v", err)
	}
	var assignments []commodel.Assignment
	if err := client.Call(context.Background(), alice, "assignment.list", nil, &assignments); err != nil || len(assignments) != 1 {
		t.Fatalf("assignment list: %+v, %v", assignments, err)
	}
	var loaded commodel.Assignment
	if err := client.Call(context.Background(), alice, "assignment.load", map[string]any{"id": assignment.AssignmentID}, &loaded); err != nil || loaded.AssignmentID != assignment.AssignmentID {
		t.Fatalf("assignment load: %+v, %v", loaded, err)
	}
	if code, body := call("control", "alice", "assignment.pause", alice, map[string]any{"id": assignment.AssignmentID, "expected_revision": assignment.Revision + 1}, ""); code != http.StatusConflict {
		t.Fatalf("stale assignment pause: %d %s", code, body)
	}
	if code, body := call("control", "alice", "assignment.pause", alice, map[string]any{"id": assignment.AssignmentID, "expected_revision": assignment.Revision}, ""); code != http.StatusOK {
		t.Fatalf("current assignment pause: %d %s", code, body)
	}
	if err := client.Call(context.Background(), alice, "assignment.load", map[string]any{"id": assignment.AssignmentID}, &loaded); err != nil || loaded.State != "paused" {
		t.Fatalf("paused assignment: %+v, %v", loaded, err)
	}
	if code, body := call("control", "bob", "assignment.load", bob, map[string]any{"id": assignment.AssignmentID}, ""); code != http.StatusNotFound {
		t.Fatalf("foreign assignment load: %d %s", code, body)
	}
	if code, body := call("wrong", "alice", "schedule.list", alice, nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong bearer: %d %s", code, body)
	}
	if code, body := call("control", "bob", "schedule.list", alice, nil, ""); code != http.StatusBadRequest {
		t.Fatalf("foreign caller assertion: %d %s", code, body)
	}
	if code, body := call("control", "alice", "schedule.list", alice, nil, "https://example.test"); code != http.StatusForbidden {
		t.Fatalf("browser origin: %d %s", code, body)
	}
	for _, tc := range []struct {
		principal string
		caller    identity.Envelope
		want      int
	}{
		{"alice", alice, 1},
		{"bob", bob, 0},
	} {
		code, body := call("control", tc.principal, "schedule.list", tc.caller, nil, "")
		if code != http.StatusOK {
			t.Fatalf("list %s: %d %s", tc.principal, code, body)
		}
		var result struct {
			Result []commodel.Schedule `json:"result"`
		}
		if json.Unmarshal(body, &result) != nil || len(result.Result) != tc.want {
			t.Fatalf("list %s: %s", tc.principal, body)
		}
	}
	h.revoke(t, "alice", "read")
	h.now = h.now.Add(2 * time.Minute)
	if code, body := call("control", "alice", "schedule.list", alice, nil, ""); code != http.StatusForbidden {
		t.Fatalf("revoked read: %d %s", code, body)
	}
}

func TestAutomationControlHTTPReportsBoundResult(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	host := h.host(t)
	job := commodel.Job{Envelope: alice, ID: "job-http-result", IdempotencyKey: "occurrence-http-result", UserID: "alice", Channel: "telegram_bot", ChatID: 11}
	if _, err := host.queue.admit(job, h.now); err != nil {
		t.Fatal(err)
	}
	if err := host.tasks.put(commodel.Task{TaskID: job.ID, PrincipalID: "alice", Channel: job.Channel, ChatID: job.ChatID}); err != nil {
		t.Fatal(err)
	}
	h.comm.down.Store(true)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	request := map[string]any{"job_id": job.ID, "status": "completed", "text": "done"}
	for i := 0; i < 2; i++ {
		if err := client.Call(context.Background(), alice, "result.report", request, nil); err != nil {
			t.Fatalf("result handoff %d: %v", i, err)
		}
	}
	if pendingDeliveries(t, host, "alice") != 1 {
		t.Fatal("result replay created or lost a delivery")
	}
	if err := client.Call(context.Background(), h.envelope(t, "bob"), "result.report", request, nil); err == nil {
		t.Fatal("foreign principal reported another owner's job")
	}
}

func TestAutomationControlHTTPReadsAndEditsOwnedSchedule(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.envelope(t, "alice"), h.envelope(t, "bob")
	h.grant(t, "alice", "read")
	h.grant(t, "alice", "write")
	h.grant(t, "bob", "read")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	created, err := host.CreateSchedule(context.Background(), alice, draft("rt-edit-http", alice), "")
	if err != nil {
		t.Fatal(err)
	}
	var card struct {
		Schedule commodel.Schedule `json:"schedule"`
		State    string            `json:"state"`
	}
	if err := client.Call(context.Background(), alice, "schedule.card", map[string]any{"id": created.ScheduleID}, &card); err != nil || card.State != "active" || card.Schedule.ScheduleID != created.ScheduleID {
		t.Fatalf("owner card: %+v, %v", card, err)
	}
	var loaded commodel.Schedule
	if err := client.Call(context.Background(), alice, "schedule.load", map[string]any{"id": created.ScheduleID}, &loaded); err != nil || loaded.Revision != created.Revision {
		t.Fatalf("owner read: %+v, %v", loaded, err)
	}
	if err := client.Call(context.Background(), bob, "schedule.load", map[string]any{"id": created.ScheduleID}, &loaded); err == nil {
		t.Fatal("foreign schedule read succeeded")
	}
	patch := map[string]any{"input": "changed", "expected_revision": created.Revision}
	var edited commodel.Schedule
	if err := client.Call(context.Background(), alice, "schedule.edit", map[string]any{"id": created.ScheduleID, "patch": patch}, &edited); err != nil || edited.Input != "changed" || edited.Revision <= created.Revision {
		t.Fatalf("owner edit: %+v, %v", edited, err)
	}
	if err := client.Call(context.Background(), alice, "schedule.edit", map[string]any{"id": created.ScheduleID, "patch": patch}, &edited); err == nil {
		t.Fatal("stale revision edited schedule")
	}
	if err := host.DeleteSchedule(context.Background(), alice, created.ScheduleID, edited.Revision); err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), alice, "schedule.card", map[string]any{"id": created.ScheduleID}, &card); err != nil || card.State != "deleted" {
		t.Fatalf("retired card: %+v, %v", card, err)
	}
}

func TestAutomationProgressRequiresLiveBoundSupervisorCall(t *testing.T) {
	h := newHarness(t)
	alice := h.envelope(t, "alice")
	h.grant(t, "alice", "write")
	host := h.host(t)
	e, err := host.engineFor("alice")
	if err != nil {
		t.Fatal(err)
	}
	assignment := commodel.Assignment{AssignmentID: "asg-progress", Revision: 1, Envelope: alice, State: "active", Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1}
	if err := writeJSONAtomic(e.AssignmentPath(assignment.AssignmentID), assignment); err != nil {
		t.Fatal(err)
	}
	schedule := draft("rt-progress", alice)
	schedule.AssignmentID, schedule.AssignmentPlanRevision = assignment.AssignmentID, 1
	created, err := host.CreateSchedule(context.Background(), alice, schedule, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	request := map[string]any{"id": assignment.AssignmentID, "expected_revision": assignment.Revision, "job_id": "unknown", "summary": "found the answer"}
	if err := client.Call(context.Background(), alice, "assignment.progress", request, nil); err == nil {
		t.Fatal("unbound progress accepted")
	}
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	h.sup.blockAlice = release
	h.now = created.NextDue.Add(time.Second)
	done := make(chan error, 1)
	go func() { done <- host.Tick(context.Background(), h.now) }()
	var job commodel.Job
	deadline := time.After(5 * time.Second)
	for job.ID == "" {
		host.inFlight.Range(func(_, value any) bool { job = value.(commodel.Job); return false })
		select {
		case <-deadline:
			t.Fatal("supervisor call never became live")
		case <-time.After(5 * time.Millisecond):
		}
	}
	request["job_id"] = job.ID
	request["id"] = "other-assignment"
	if err := client.Call(context.Background(), alice, "assignment.progress", request, nil); err == nil {
		t.Fatal("job redirected progress to another assignment")
	}
	request["id"] = assignment.AssignmentID
	var updated commodel.Assignment
	if err := client.Call(context.Background(), alice, "assignment.progress", request, &updated); err != nil || updated.Progress.LastJobID != job.ID || updated.Progress.Summary != "found the answer" {
		t.Fatalf("bound progress: %+v, %v", updated, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var replay commodel.Assignment
	if err := client.Call(context.Background(), alice, "assignment.progress", request, &replay); err != nil || replay.Revision != updated.Revision {
		t.Fatalf("terminal progress replay: %+v, %v", replay, err)
	}
}

func TestAutomationControlHTTPIntakesOwnedHook(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.envelope(t, "alice"), h.envelope(t, "bob")
	h.grant(t, "alice", "write")
	h.grant(t, "bob", "write")
	host := h.host(t)
	schedule := draft("rt-http-hook", alice)
	schedule.JobKind = "event"
	schedule.Probe = &commodel.WatchProbe{Kind: "hook", URL: "hook:github"}
	created, err := host.CreateSchedule(context.Background(), alice, schedule, "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	request := map[string]any{"id": created.ScheduleID, "body": []byte(`{"event":"one"}`)}
	if err := client.Call(context.Background(), bob, "hook.intake", request, nil); err == nil {
		t.Fatal("foreign hook intake was accepted")
	}
	if err := client.Call(context.Background(), alice, "hook.intake", request, nil); err != nil {
		t.Fatalf("owner hook intake: %v", err)
	}
	engine, err := host.engineFor("alice")
	if err != nil || engine.HookPendingCount(created.ScheduleID) != 1 {
		t.Fatalf("hook payload not durable: %v", err)
	}
}
