//go:build integration

package main

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/commops"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// Exercise the real gateway -> ToolHub -> durable owner engine without Docker.
func TestM32AssignmentReconcileAndRecovery(t *testing.T) {
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "assignment")
	m32Require(t, err)
	h := newM32Lifecycle(t, t.Context(), "", "prod", f)
	stop := h.startToolHub(t, t.Context())
	t.Cleanup(func() { stop() })
	control := commops.ControlClient{URL: h.toolURL, Token: h.control}
	items := make([]commodel.Assignment, len(f.Users))
	for i, user := range f.Users {
		plan := commodel.AssignmentPlan{Revision: 1, Sources: []commodel.AssignmentSource{{Name: "reminder", Schedule: commodel.RoutineDraft{Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "0 0 1 1 *", JobKind: "reminder", Input: "M32 future assignment"}}}}
		body, err := json.Marshal(map[string]any{"title": "M32 reconcile", "intent": "M32 reconcile " + user, "apply": true, "plan": plan})
		m32Require(t, err)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, os.Getenv("HUB_COMMUNICATION_CONTROL_URL")+"/v1/assignments", strings.NewReader(string(body)))
		m32Require(t, err)
		req.Header.Set("Authorization", "Bearer "+h.tokens[user])
		req.Header.Set("X-Hub-Principal", user)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		m32Require(t, err)
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("assignment create/apply HTTP %d", resp.StatusCode)
		}
		m32Require(t, json.NewDecoder(resp.Body).Decode(&items[i]))
		m32Require(t, resp.Body.Close())
		item := items[i]
		if item.State != "active" || item.AppliedRev != 1 || len(item.Applied) != 1 || item.Envelope.PrincipalID != user {
			t.Fatal("assignment lost its owner, plan revision or provisioning receipt")
		}
		var replay commodel.Assignment
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.apply", map[string]any{"id": item.AssignmentID, "expected_revision": item.Revision}, &replay))
		if replay.Applied["reminder"].ScheduleID != item.Applied["reminder"].ScheduleID {
			t.Fatal("replayed apply provisioned a second source")
		}
		if err := control.Call(t.Context(), f.Auth[user], "assignment.pause", map[string]any{"id": item.AssignmentID, "expected_revision": item.Revision}, nil); err == nil {
			t.Fatal("stale assignment mutation accepted")
		}
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.pause", map[string]any{"id": item.AssignmentID, "expected_revision": replay.Revision}, nil))
	}
	stop()
	stop = h.startToolHub(t, t.Context())
	for i, user := range f.Users {
		id := items[i].AssignmentID
		var item commodel.Assignment
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.load", map[string]any{"id": id}, &item))
		var source commodel.Schedule
		m32Require(t, control.Call(t.Context(), f.Auth[user], "schedule.load", map[string]any{"id": item.Applied["reminder"].ScheduleID}, &source))
		if item.State != "paused" || !source.Paused || source.AssignmentID != id || source.Envelope.PrincipalID != user {
			t.Fatal("restart lost the assignment pause fence or source ownership")
		}
		if err := control.Call(t.Context(), f.Auth[f.Users[1-i]], "assignment.load", map[string]any{"id": id}, nil); err == nil {
			t.Fatal("foreign assignment readable")
		}
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.resume", map[string]any{"id": id, "expected_revision": item.Revision}, nil))
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.load", map[string]any{"id": id}, &item))
		m32Require(t, control.Call(t.Context(), f.Auth[user], "schedule.load", map[string]any{"id": source.ScheduleID}, &source))
		if item.State != "active" || source.Paused {
			t.Fatal("resume did not reactivate the owned source")
		}
		if err := control.Call(t.Context(), f.Auth[user], "assignment.progress", map[string]any{"id": id, "expected_revision": item.Revision, "job_id": "invented", "summary": "unearned progress"}, nil); err == nil {
			t.Fatal("progress accepted without a live specialist job")
		}
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.complete", map[string]any{"id": id, "expected_revision": item.Revision}, &item))
		if item.State != "completed" {
			t.Fatal("assignment did not complete")
		}
		m32Require(t, control.Call(t.Context(), f.Auth[user], "assignment.delete", map[string]any{"id": id, "expected_revision": item.Revision}, nil))
		if err := control.Call(t.Context(), f.Auth[user], "schedule.load", map[string]any{"id": source.ScheduleID}, nil); err == nil {
			t.Fatal("assignment removal left a live source")
		}
	}
}

func (h *m32Lifecycle) assignmentWatchExecution(t *testing.T, ctx context.Context, restartToolHub func()) {
	t.Helper()
	control := commops.ControlClient{URL: h.toolURL, Token: h.control}
	items := make([]commodel.Assignment, len(h.f.Users))
	watchIDs := make([]string, len(h.f.Users))
	before := map[string]bool{}
	for _, record := range h.deliveryRecords(t) {
		before[record.Delivery.ID] = true
	}
	for i, user := range h.f.Users {
		h.call(t, ctx, user, api.RunContext{}, "file_write", map[string]any{"root": "workspace", "path": "m32-source.txt", "text": "M32-source-" + user}, true)
		args := map[string]any{"root": "workspace", "path": "m32-source.txt"}
		watchIDs[i] = "m32-watch-" + user
		h.call(t, ctx, user, api.RunContext{}, "watch_create", map[string]any{"id": watchIDs[i], "timezone": "UTC", "expression": "0 0 1 1 *", "text": "M32 watch evidence", "tool": "file_read", "args": args, "sink": "agent", "delivery": "stream", "filter": `emit("keep")`, "min_interval_seconds": 5}, true)
		var sample struct {
			Sources []struct {
				Error    string                `json:"error"`
				Events   []struct{ ID string } `json:"events"`
				Verdicts map[string]string     `json:"verdicts"`
			} `json:"sources"`
		}
		for _, verdict := range []string{"keep", "drop"} {
			raw := h.call(t, ctx, user, api.RunContext{}, "watch_sample", map[string]any{"id": watchIDs[i], "filter": `emit("` + verdict + `")`}, true)
			m32Require(t, json.Unmarshal(m32ToolPayload(t, raw), &sample))
			if len(sample.Sources) != 1 || sample.Sources[0].Error != "" || len(sample.Sources[0].Events) != 1 || sample.Sources[0].Verdicts[sample.Sources[0].Events[0].ID] != verdict {
				t.Fatal("real source/filter sample lost the event or verdict")
			}
		}
		plan := commodel.AssignmentPlan{Revision: 1, Specialist: &commodel.SpecialistSpec{Instructions: "M32 specialist evidence review"}, Sources: []commodel.AssignmentSource{{Name: "owner-file", Schedule: commodel.RoutineDraft{Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "0 0 1 1 *", Input: "M32 assignment evidence", JobKind: "event", Sink: "agent", Delivery: "stream", Probe: &commodel.WatchProbe{Kind: "mcp_tool", Tool: "file_read", Args: args, Filter: `emit("keep")`, MinIntervalSeconds: 5}}}}}
		raw := h.call(t, ctx, user, api.RunContext{}, "assignment_create", map[string]any{"title": "M32 specialist", "text": "M32 assignment " + user, "plan": plan, "queue": true}, true)
		m32Require(t, json.Unmarshal(m32ToolPayload(t, raw), &items[i]))
		if items[i].State != "active" || len(items[i].Applied) != 1 {
			t.Fatal("assignment did not provision its deterministic source")
		}
		h.call(t, ctx, h.f.Users[1-i], api.RunContext{}, "assignment_read", map[string]any{"id": items[i].AssignmentID}, false)
		h.call(t, ctx, h.f.Users[1-i], api.RunContext{}, "watch_edit", map[string]any{"id": watchIDs[i], "action": "pause"}, false)
	}
	seen := map[int64]int{}
	for range 2 * len(h.f.Users) {
		select {
		case chat := <-h.channelSends:
			seen[chat]++
		case <-time.After(time.Minute):
			t.Fatal("watch/assignment did not finish its actual agent wake and delivery")
		case <-ctx.Done():
			t.Fatal("watch/assignment acceptance cancelled")
		}
	}
	if seen[11] != 2 || seen[12] != 2 || len(seen) != 2 {
		t.Fatal("watch/assignment delivery duplicated or crossed its owner")
	}
	m32Wait(t, ctx, 15*time.Second, func() bool {
		count := 0
		for _, record := range h.deliveryRecords(t) {
			if before[record.Delivery.ID] {
				continue
			}
			if !record.Sent {
				return false
			}
			count++
			run, err := (&api.SupervisorRunAuthority{URL: h.supervisor.URL, Token: h.control}).Lookup(ctx, record.Delivery.JobID)
			m32Require(t, err)
			if run.Status != "completed" || run.Envelope.PrincipalID != record.Principal || record.Delivery.Text != "M32 acceptance complete" {
				t.Fatal("watch/assignment result lacks a completed owner-bound agent run")
			}
		}
		return count == 2*len(h.f.Users)
	})
	restartToolHub()
	select {
	case <-h.channelSends:
		t.Fatal("watch/assignment source was replayed after restart")
	case <-time.After(7 * time.Second):
	case <-ctx.Done():
		t.Fatal("watch/assignment restart acceptance cancelled")
	}
	for i, user := range h.f.Users {
		var item commodel.Assignment
		m32Require(t, control.Call(ctx, h.f.Auth[user], "assignment.load", map[string]any{"id": items[i].AssignmentID}, &item))
		if item.Checkpoint.LLMCalls != 1 || item.Checkpoint.WakesToday != 1 || item.Checkpoint.LastError != "" {
			t.Fatal("assignment wake checkpoint was lost or charged twice")
		}
		h.call(t, ctx, user, api.RunContext{}, "assignment_edit", map[string]any{"id": item.AssignmentID, "action": "pause", "expected_revision": item.Revision}, true)
		h.call(t, ctx, user, api.RunContext{}, "watch_edit", map[string]any{"id": watchIDs[i], "action": "pause"}, true)
		m32Require(t, control.Call(ctx, h.f.Auth[user], "assignment.load", map[string]any{"id": item.AssignmentID}, &item))
		h.call(t, ctx, user, api.RunContext{}, "assignment_remove", map[string]any{"id": item.AssignmentID, "expected_revision": item.Revision}, true)
		h.call(t, ctx, user, api.RunContext{}, "watch_remove", map[string]any{"id": watchIDs[i]}, true)
	}
	t.Log("passed: actual file source, isolated keep/drop filter, watch and assignment agent wakes, owner deliveries, durable checkpoint and restart deduplication")
}

func m32ToolPayload(t *testing.T, raw string) json.RawMessage {
	t.Helper()
	var reply struct {
		Structured json.RawMessage         `json:"structuredContent"`
		Content    []struct{ Text string } `json:"content"`
	}
	m32Require(t, json.Unmarshal([]byte(raw), &reply))
	data := reply.Structured
	if len(data) == 0 && len(reply.Content) == 1 {
		data = json.RawMessage(reply.Content[0].Text)
	}
	var body struct {
		Result json.RawMessage `json:"result"`
	}
	m32Require(t, json.Unmarshal(data, &body))
	if len(body.Result) == 0 {
		t.Fatal("tool response has no result")
	}
	return body.Result
}
