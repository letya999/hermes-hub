package communication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestRoutineConcurrentEditsCommitOneRevision(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			patch := map[string]json.RawMessage{"expected_revision": json.RawMessage(`1`), "input": json.RawMessage(fmt.Sprintf(`"edit-%d"`, i))}
			_, err := s.editRoutine(in.ScheduleID, in.Envelope, patch, false, time.Now().UTC())
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrRoutineRevision) {
			t.Fatal(err)
		}
	}
	card, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || success != 1 || card.Schedule.Revision != 2 {
		t.Fatalf("concurrent edits: success=%d card=%+v err=%v", success, card, err)
	}
}

func TestRoutineSourceEditSeedsANewCheckBaseline(t *testing.T) {
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new-source-value")) }))
	defer source.Close()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.watchHTTP = source.Client()
	env := c.Users[0].Envelope(11)
	in := Schedule{ScheduleID: "change-source", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "check", Probe: &WatchProbe{URL: source.URL + "/old"}, Expect: "changed"}
	in, err = g.spool.CreateSchedule(in, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SeedWatchBaseline(in.ScheduleID, "old-source-value", time.Now()); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_revision":1,"probe":{"url":%q}}`, source.URL+"/new")
	r := httptest.NewRequest(http.MethodPost, "/v1/routines/"+in.ScheduleID, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("source edit: %d %s", w.Code, w.Body.String())
	}
	got, err := g.spool.GetRoutine(in.ScheduleID, env)
	if err != nil || got.Schedule.InitialProbeValue == nil || *got.Schedule.InitialProbeValue != "new-source-value" || got.WatchState.Value != "new-source-value" || got.WatchState.Baseline != watchHash("new-source-value") {
		t.Fatalf("stale source baseline: %+v %v", got, err)
	}
}

func TestRoutineKindConversionClearsEventSettings(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "convert-event", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "read_events"}, Sink: "agent", Delivery: "stream"}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{"job_kind": json.RawMessage(`"reminder"`)}, false, time.Now().UTC())
	if err != nil || got.Sink != "" || got.Delivery != "" || got.Probe != nil {
		t.Fatalf("event settings survived conversion: %+v %v", got, err)
	}
}

func TestRoutineKindConversionClearsDigestSources(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "convert-digest", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "digest", SourceIDs: []string{"watch-a"}}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{"job_kind": json.RawMessage(`"reminder"`)}, false, time.Now().UTC())
	if err != nil || len(got.SourceIDs) != 0 {
		t.Fatalf("digest sources survived conversion: %+v %v", got, err)
	}
}

func TestRoutineDSTWindowsStayInsideLocalBounds(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ from, open, close, want string }{
		{"2026-03-08T05:00:00Z", "02:00", "04:00", "2026-03-08T07:00:00Z"},
		{"2026-11-01T05:40:00Z", "01:15", "01:45", "2026-11-01T06:15:00Z"},
	} {
		from, _ := time.Parse(time.RFC3339, tc.from)
		got, err := nextSchedule(Schedule{Expression: "every:15m", Timing: &RoutineTiming{Windows: []RoutineWindow{{From: tc.open, To: tc.close}}}}, loc, from)
		if err != nil || got.Format(time.RFC3339) != tc.want {
			t.Errorf("window %s..%s from %s: %s %v, want %s", tc.open, tc.close, tc.from, got, err, tc.want)
		}
	}
}

func TestRoutineStreamPauseFencesInFlightBatch(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "stream-fence", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "read_events"}, Delivery: "stream"}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PauseSchedule(in.ScheduleID, in.Envelope); err != nil {
		t.Fatal(err)
	}
	_, blocked := s.settleStreamEvents(in, []byte(`{"events":[{"id":"one","text":"stale batch"}]}`), time.Now().UTC(), nil, func(Job) error { return nil })
	d, err := s.ClaimDelivery()
	if err != nil || !blocked || d != nil {
		t.Fatalf("paused stream admitted stale batch: blocked=%v delivery=%+v err=%v", blocked, d, err)
	}
}

func TestRoutineStreamEditFencesInFlightBatch(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "stream-edit-fence", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "read_events"}, Delivery: "stream"}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{"input": json.RawMessage(`"new text"`)}, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, blocked := s.settleStreamEvents(in, []byte(`{"events":[{"id":"one","text":"stale batch"}]}`), time.Now().UTC(), nil, func(Job) error { return nil })
	d, err := s.ClaimDelivery()
	if err != nil || !blocked || d != nil {
		t.Fatalf("old revision admitted stale batch: blocked=%v delivery=%+v err=%v", blocked, d, err)
	}
}

func TestRoutineEventSourceEditHasIndependentDeduplication(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "stream-source-edit", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "read_events"}, Delivery: "stream"}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"events":[{"id":"one","text":"source event"}]}`)
	if _, blocked := s.settleStreamEvents(in, body, time.Now().UTC(), nil, func(Job) error { return nil }); blocked {
		t.Fatal("first source blocked")
	}
	first, err := s.ClaimDelivery()
	if err != nil || first == nil {
		t.Fatalf("first delivery: %+v %v", first, err)
	}
	if err := s.CompleteDelivery(first.ID); err != nil {
		t.Fatal(err)
	}
	in, err = s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{"probe": json.RawMessage(`{"kind":"mcp_tool","tool":"other_events"}`)}, false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, blocked := s.settleStreamEvents(in, body, time.Now().UTC(), nil, func(Job) error { return nil }); blocked {
		t.Fatal("new source blocked")
	}
	next, err := s.ClaimDelivery()
	if err != nil || next == nil || next.ID == first.ID {
		t.Fatalf("different source lost same native event id: %+v %v", next, err)
	}
}

func TestRoutineVersionedActionReturnsDurableCard(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	for _, action := range []string{"pause", "snooze", "delete"} {
		in := Schedule{ScheduleID: "receipt-" + action, Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "reminder"}
		if _, err := g.spool.CreateSchedule(in, env, ""); err != nil {
			t.Fatal(err)
		}
		method, path := http.MethodPost, "/v1/routines/"+in.ScheduleID+"/"+action
		if action == "delete" {
			method, path = http.MethodDelete, "/v1/routines/"+in.ScheduleID
		}
		r := httptest.NewRequest(method, path, strings.NewReader(`{"expected_revision":1,"until":"2100-01-01T10:00:00Z"}`))
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		var got RoutineCard
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Schedule.Revision != 2 || got.Schedule.ScheduleID != in.ScheduleID {
			t.Errorf("%s has no durable acknowledgement: %d %s", action, w.Code, w.Body.String())
		}
	}
}

func TestRoutinePreviewStopsAtIntervalEnd(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	in := Schedule{ScheduleID: "preview-end", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder", Timing: &RoutineTiming{EndAt: now.Add(45 * time.Minute)}}
	in, err := s.createScheduleAt(in, in.Envelope, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PreviewRoutine(in.ScheduleID, in.Envelope, 20, now)
	if err != nil || len(got.Times) != 2 || !got.Times[1].Equal(now.Add(30*time.Minute)) {
		t.Fatalf("finite preview failed: %+v %v", got, err)
	}
}

func TestRoutinePreviewRetiredHasNoFutureSlots(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	if err := s.DeleteSchedule(in.ScheduleID, in.Envelope); err != nil {
		t.Fatal(err)
	}
	got, err := s.PreviewRoutine(in.ScheduleID, in.Envelope, 20, in.NextDue.Add(-time.Minute))
	raw, _ := json.Marshal(got)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if err != nil || len(got.Times) != 0 || fields["state"] != "deleted" {
		t.Fatalf("retired preview advertises execution: %s %v", raw, err)
	}
}

func TestRoutineHistoryAggregatesOwnedMultiMessageDelivery(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	o.ScheduleID = in.ScheduleID
	o.Revision = in.Revision
	o.JobKind = "script"
	o.DueAt = in.NextDue
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(o)
	for _, suffix := range []string{"a", "b"} {
		if err := s.EnqueueDelivery(Delivery{ID: "script-" + id + suffix, OccurrenceID: id, Channel: "telegram_bot", ChatID: 11, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, Text: "fixture"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnqueueDelivery(Delivery{ID: "foreign-evidence", OccurrenceID: id, Channel: "telegram_bot", ChatID: 22, ConversationID: "telegram-22", DeliveryTargetID: "telegram-22", Text: "foreign"}); err != nil {
		t.Fatal(err)
	}
	for step, want := range []string{"queued", "partial", "uncertain"} {
		if step > 0 {
			d, err := s.ClaimDelivery()
			if err != nil || d == nil {
				t.Fatalf("claim: %+v %v", d, err)
			}
			if d.ID == "foreign-evidence" {
				if err := s.CompleteDelivery(d.ID); err != nil {
					t.Fatal(err)
				}
				d, err = s.ClaimDelivery()
				if err != nil || d == nil {
					t.Fatal(err)
				}
			}
			if step == 1 {
				err = s.CompleteDelivery(d.ID)
			} else {
				err = s.UncertainDelivery(d.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		runs, err := s.RoutineHistory(in.ScheduleID, in.Envelope)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs: %+v %v", runs, err)
		}
		raw, _ := json.Marshal(runs[0])
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		if fields["delivery_state"] != want || fields["delivery_count"] != float64(2) {
			t.Errorf("owned multipart outcome %s: %s", want, raw)
		}
	}
}

func TestRoutineHistoryConfirmsAllMessagesDelivered(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	o.ScheduleID = in.ScheduleID
	o.Revision = in.Revision
	o.JobKind = "script"
	o.DueAt = in.NextDue
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	id := occurrenceID(o)
	for _, suffix := range []string{"a", "b"} {
		if err := s.EnqueueDelivery(Delivery{ID: "script-" + id + suffix, OccurrenceID: id, Channel: "telegram_bot", ChatID: 11, ConversationID: o.Job.ConversationID, DeliveryTargetID: o.Job.DeliveryTargetID, Text: "fixture"}); err != nil {
			t.Fatal(err)
		}
		d, err := s.ClaimDelivery()
		if err != nil || d == nil {
			t.Fatal(err)
		}
		if err := s.CompleteDelivery(d.ID); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.RoutineHistory(in.ScheduleID, in.Envelope)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	raw, _ := json.Marshal(runs[0])
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["delivery_state"] != "delivered" || fields["delivery_count"] != float64(2) {
		t.Fatalf("not all messages confirmed: %s", raw)
	}
}

func TestRoutineHistoryReadsOnlyMatchingAgentExecution(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	o.ScheduleID = in.ScheduleID
	o.Revision = in.Revision
	o.JobKind = "agent"
	o.DueAt = in.NextDue
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	job := o.Job
	job.ConversationID = routineConversation(in.ScheduleID)
	mapping := mappingFromJob(job, time.Now().UTC())
	mapping.Status = "completed"
	if err := s.EnqueueDelivery(Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: "fixture"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	for step, want := range []string{"completed", "unavailable"} {
		if step == 1 {
			mapping.PrincipalID = "bob"
		}
		if err := atomicJSON(s.mappingPath(job.ID), mapping); err != nil {
			t.Fatal(err)
		}
		runs, err := s.RoutineHistory(in.ScheduleID, in.Envelope)
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs: %+v %v", runs, err)
		}
		encoded, _ := json.Marshal(runs[0])
		var fields map[string]any
		_ = json.Unmarshal(encoded, &fields)
		if fields["execution_state"] != want || fields["delivery_state"] != "delivered" {
			t.Errorf("execution evidence %s: %s", want, encoded)
		}
	}
}

func TestRoutineCardExposesEventSubscriptionHealth(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "event-health", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "event", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "read_events"}}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.eventPath(in.ScheduleID), EventState{ScheduleID: in.ScheduleID, Degraded: true, LastError: "connector unauthorized"}); err != nil {
		t.Fatal(err)
	}
	card, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(card)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	state, ok := fields["event_state"].(map[string]any)
	if !ok || state["degraded"] != true || state["last_error"] != "connector unauthorized" {
		t.Fatalf("subscription health hidden: %s", raw)
	}
}

func TestRoutineLifecycleKeepsPrincipalAndContextIsolation(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	otherContext := in.Envelope
	otherContext.ContextID = "other-context"
	otherPrincipal := in.Envelope
	otherPrincipal.PrincipalID = "bob"
	for _, caller := range []identity.Envelope{otherContext, otherPrincipal} {
		if _, err := s.GetRoutine(in.ScheduleID, caller); !errors.Is(err, ErrRoutineNotOwned) {
			t.Errorf("foreign get: %v", err)
		}
		if _, err := s.RoutineHistory(in.ScheduleID, caller); !errors.Is(err, ErrRoutineNotOwned) {
			t.Errorf("foreign history: %v", err)
		}
		if _, err := s.PreviewRoutine(in.ScheduleID, caller, 3, time.Now()); !errors.Is(err, ErrRoutineNotOwned) {
			t.Errorf("foreign preview: %v", err)
		}
		if _, err := s.editRoutine(in.ScheduleID, caller, map[string]json.RawMessage{"input": json.RawMessage(`"foreign"`)}, true, time.Now()); !errors.Is(err, ErrRoutineNotOwned) {
			t.Errorf("foreign edit/resume: %v", err)
		}
		if err := s.PauseSchedule(in.ScheduleID, caller, 1); err == nil {
			t.Error("foreign pause accepted")
		}
		if err := s.SnoozeSchedule(in.ScheduleID, caller, time.Now().Add(time.Hour), 1); err == nil {
			t.Error("foreign snooze accepted")
		}
		if err := s.DeleteSchedule(in.ScheduleID, caller, 1); err == nil {
			t.Error("foreign delete accepted")
		}
	}
	got, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || got.Schedule.Revision != 1 || got.Schedule.Input != in.Input || got.Schedule.Paused {
		t.Fatalf("foreign operations changed state: %+v %v", got, err)
	}
}

func TestRoutineCommandDistinguishesCompletedSchedule(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	in := reminderSchedule(t, g.spool, RoutineOccurrence{Job: Job{Envelope: c.Users[0].Envelope(11)}}, "once:2100-01-01T10:00:00+03:00", 0)
	g.now = func() time.Time { return in.NextDue }
	if err := g.spool.TickSchedules(in.NextDue, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), taskUpdate(1, 1, "/routines", 11, 0)); err != nil {
		t.Fatal(err)
	}
	reply, err := g.spool.ClaimDelivery()
	if err != nil || reply == nil || !strings.Contains(reply.Text, "завершено") || strings.Contains(reply.Text, "0001-") || strings.Contains(reply.Text, ", пауза") {
		t.Fatalf("completed routine misrepresented: %+v %v", reply, err)
	}
}

func TestRoutinePatchRejectsNullRequiredFields(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	for _, field := range []string{"input", "expression", "timezone", "job_kind"} {
		if _, err := s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{field: json.RawMessage(`null`)}, false, time.Now()); err == nil {
			t.Errorf("null %s silently accepted", field)
		}
	}
	got, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || got.Schedule.Revision != 1 {
		t.Fatalf("invalid patches mutated revision: %+v %v", got, err)
	}
}

func TestRoutineReadsShowEffectiveLegacySnoozeDeadline(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	in.SnoozedUntil = in.NextDue.Add(3 * time.Hour)
	if err := atomicJSON(s.schedulePath(in.ScheduleID), in); err != nil {
		t.Fatal(err)
	}
	card, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || !card.Schedule.NextDue.Equal(in.SnoozedUntil) {
		t.Errorf("card advertises premature deadline: %+v %v", card, err)
	}
	list, err := s.ListSchedules(in.Envelope)
	if err != nil || len(list) != 1 || !list[0].NextDue.Equal(in.SnoozedUntil) {
		t.Errorf("list advertises premature deadline: %+v %v", list, err)
	}
}

func TestRoutineHistorySurvivesDetailCompaction(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.config.ControlAuth = "control-token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}, RuntimeID: "alice", PolicyVersion: "policy-1"}}
	in := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", 0)
	g.tickProactive(in.NextDue)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("delivery: %+v %v", d, err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(s.root, "occurrences", d.OccurrenceID+".json"), filepath.Join(s.root, "outbox", "done", d.ID+".json")} {
		if err := os.Chtimes(path, in.NextDue, in.NextDue); err != nil {
			t.Fatal(err)
		}
	}
	for _, age := range []time.Duration{0, time.Hour, 49 * time.Hour} {
		if age > 0 {
			if err := s.Compact(in.NextDue.Add(age), 14*24*time.Hour, CompactLimits{}); err != nil {
				t.Fatal(err)
			}
		}
		if age == 49*time.Hour {
			if _, err := os.Stat(filepath.Join(s.root, "occurrences", "archive", d.OccurrenceID+".json")); !os.IsNotExist(err) {
				t.Fatal("detail compaction fixture did not expire")
			}
		}
		r := httptest.NewRequest(http.MethodGet, "/v1/routines/"+in.ScheduleID+"/history?limit=20", nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Errorf("history at %s: %d %s", age, w.Code, w.Body.String())
			continue
		}
		var got struct {
			Items []struct {
				ID       string    `json:"occurrence_id"`
				Revision uint64    `json:"revision"`
				Due      time.Time `json:"due_at"`
				Kind     string    `json:"job_kind"`
				State    string    `json:"state"`
				Delivery string    `json:"delivery_state"`
			} `json:"items"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != 1 {
			t.Fatalf("history lost: %s", w.Body.String())
		}
		run := got.Items[0]
		if run.ID != d.OccurrenceID || run.Revision != in.Revision || !run.Due.Equal(in.NextDue) || run.Kind != "reminder" || run.State != "dispatched" || run.Delivery != "delivered" {
			t.Fatalf("history lost receipt detail: %+v", run)
		}
	}
}

func TestRoutineHistoryPagesNewestFirst(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.config.ControlAuth = "control-token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}, RuntimeID: "alice", PolicyVersion: "policy-1"}}
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	for i := 0; i < 5; i++ {
		copy := o
		copy.ScheduleID = in.ScheduleID
		copy.Revision = in.Revision
		copy.DueAt = in.NextDue.Add(time.Duration(i) * time.Minute)
		copy.JobKind = "reminder"
		if err := s.PutOccurrence(copy, copy.Job.Envelope); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	total := 0
	for page := 0; page < 3; page++ {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines/"+in.ScheduleID+"/history?limit=2&after="+cursor, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		var got struct {
			Items []RoutineRun `json:"items"`
			Next  string       `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Items) == 0 || len(got.Items) > 2 || !got.Items[0].DueAt.Equal(in.NextDue.Add(time.Duration(4-total)*time.Minute)) {
			t.Fatalf("unbounded/unordered history: %s", w.Body.String())
		}
		total += len(got.Items)
		cursor = got.Next
	}
	if total != 5 || cursor != "" {
		t.Fatalf("history lost rows: %d cursor %s", total, cursor)
	}
}

func TestRoutineHistoryRejectsInvalidPageSize(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.config.ControlAuth = "control-token"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}, RuntimeID: "alice", PolicyVersion: "policy-1"}}
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	for _, limit := range []string{"0", "-1", "101", "bad"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines/"+in.ScheduleID+"/history?limit="+limit, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("history limit %s: %d", limit, w.Code)
		}
	}
}

func TestRoutineDowntimePastIntervalEndCompletes(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	in := Schedule{ScheduleID: "expired-window", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder", Timing: &RoutineTiming{EndAt: now.Add(time.Hour)}}
	in, err := s.createScheduleAt(in, in.Envelope, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(now.Add(24*time.Hour), "", nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || got.State != "completed" || !got.Schedule.NextDue.IsZero() {
		t.Fatalf("expired interval remains active: %+v %v", got, err)
	}
}

func TestRoutinePatchCheckConfiguration(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"job_kind":"check","probe":{"url":"https://example.invalid/value","regex":"(.*)"},"expect":"equals:ready","max_retries":2,"notify_mode":"agent","quiet_from":"22:00","quiet_to":"08:00","daily_limit":3,"catch_up_window_ns":1800000000000}`), &patch); err != nil {
		t.Fatal(err)
	}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, patch, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("check configuration rejected: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key, want := range patch {
		var a, b any
		_ = json.Unmarshal(want, &a)
		_ = json.Unmarshal(fields[key], &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s = %s, want %s", key, fields[key], want)
		}
	}
}

func TestRoutinePatchEventConfiguration(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"job_kind":"event","probe":{"kind":"mcp_tool","tool":"read_events","args":{"count":2}},"sink":"agent","delivery":"poll"}`), &patch); err != nil {
		t.Fatal(err)
	}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, patch, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("event configuration rejected: %v", err)
	}
	if got.Sink != "agent" || got.Delivery != "poll" || got.Probe.Tool != "read_events" || got.Probe.Args["count"] != float64(2) {
		t.Fatalf("event settings lost: %+v", got)
	}
}

func TestRoutinePatchDigestSources(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	patch := map[string]json.RawMessage{"job_kind": json.RawMessage(`"digest"`), "source_ids": json.RawMessage(`["source-a","source-b"]`)}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, patch, false, time.Now().UTC())
	if err != nil || !reflect.DeepEqual(got.SourceIDs, []string{"source-a", "source-b"}) {
		t.Fatalf("digest source edit: %+v %v", got, err)
	}
}

func TestRoutinePatchTimingResetsCadence(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	patch := map[string]json.RawMessage{"expression": json.RawMessage(`"every:30m"`), "timezone": json.RawMessage(`"UTC"`), "timing": json.RawMessage(`{"windows":[{"from":"14:00","to":"16:00"}]}`)}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, patch, false, now)
	if err != nil || got.Timing == nil || !got.NextDue.Equal(now.Add(4*time.Hour)) || !got.IntervalAnchor.Equal(now) {
		t.Fatalf("timing edit: %+v %v", got, err)
	}
}

func TestRoutineKindConversionClearsOldAction(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := Schedule{ScheduleID: "convert", Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "check", Probe: &WatchProbe{URL: "https://example.invalid"}, Expect: "changed", MaxRetries: 2, NotifyMode: "agent"}
	in, err := s.CreateSchedule(in, in.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.editRoutine(in.ScheduleID, in.Envelope, map[string]json.RawMessage{"job_kind": json.RawMessage(`"reminder"`)}, false, time.Now().UTC())
	if err != nil {
		t.Fatalf("conversion rejected: %v", err)
	}
	if got.Probe != nil || got.Expect != "" || got.MaxRetries != 0 || got.NotifyMode != "" || got.InitialProbeValue != nil {
		t.Fatalf("old check settings retained: %+v", got)
	}
}

func TestRoutineLifecycleActionsRejectStaleRevision(t *testing.T) {
	for _, action := range []string{"pause", "snooze", "delete"} {
		t.Run(action, func(t *testing.T) {
			c := testConfig(t)
			c.ControlAuth = "control-token"
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			env := c.Users[0].Envelope(11)
			in := Schedule{ScheduleID: "revision-action", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "reminder"}
			in, err = g.spool.CreateSchedule(in, env, "")
			if err != nil {
				t.Fatal(err)
			}
			method, path := http.MethodPost, "/v1/routines/"+in.ScheduleID+"/"+action
			if action == "delete" {
				method, path = http.MethodDelete, "/v1/routines/"+in.ScheduleID
			}
			r := httptest.NewRequest(method, path, strings.NewReader(`{"expected_revision":99,"until":"2100-01-01T10:00:00Z"}`))
			r.Header.Set("Authorization", "Bearer control-token")
			r.Header.Set("X-Hub-Principal", "alice")
			w := httptest.NewRecorder()
			g.Handler().ServeHTTP(w, r)
			if w.Code != 409 {
				t.Errorf("stale %s: %d %s", action, w.Code, w.Body.String())
			}
			items, err := g.spool.ListSchedules(env)
			if err != nil || len(items) != 1 || items[0].Revision != in.Revision || items[0].Paused || !items[0].SnoozedUntil.IsZero() {
				t.Fatalf("stale action mutated: %+v %v", items, err)
			}
		})
	}
}

func TestRoutineGetRetiredOwnerCard(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	in := Schedule{ScheduleID: "retired-read", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: "fixture", JobKind: "reminder"}
	in, err = g.spool.CreateSchedule(in, env, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.DeleteSchedule(in.ScheduleID, env); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/routines/"+in.ScheduleID, nil)
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("retired routine unreadable: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Schedule Schedule `json:"schedule"`
		State    string   `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "deleted" || got.Schedule.Input != "fixture" || got.Schedule.Enabled || got.Schedule.Revision <= in.Revision {
		t.Fatalf("invalid retired card: %+v", got)
	}
}

func TestRoutineSummaryPaginationDoesNotLoseLargeInputs(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	for i := 0; i < 71; i++ {
		in := Schedule{ScheduleID: fmt.Sprintf("page-%03d", i), Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 10 * * 5", Input: strings.Repeat("x", 2048), JobKind: "reminder"}
		if _, err := g.spool.CreateSchedule(in, env, ""); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 4; page++ {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines?limit=20&after="+cursor, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Items []struct {
				ID       string    `json:"schedule_id"`
				State    string    `json:"state"`
				Revision uint64    `json:"revision"`
				Next     time.Time `json:"next_due"`
			} `json:"items"`
			Next string `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatalf("summary page expected: %v", err)
		}
		if len(result.Items) > 20 || len(result.Items) == 0 || w.Body.Len() > 64*1024 || strings.Contains(w.Body.String(), strings.Repeat("x", 100)) {
			t.Fatalf("unbounded or empty summary page: %d rows, %d bytes", len(result.Items), w.Body.Len())
		}
		for _, item := range result.Items {
			if seen[item.ID] || item.State != "active" || item.Revision != 1 || item.Next.IsZero() {
				t.Fatalf("invalid summary: %+v", item)
			}
			seen[item.ID] = true
		}
		cursor = result.Next
	}
	if len(seen) != 71 || cursor != "" {
		t.Fatalf("lost routines: %d, cursor=%s", len(seen), cursor)
	}
}

func TestRoutinePageRejectsInvalidLimit(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"0", "-1", "101", "bad"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines?limit="+limit, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("limit %s accepted: %d", limit, w.Code)
		}
	}
}

func TestRoutineSummaryFiltersLifecycleState(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	for i, kind := range []string{"reminder", "agent", "reminder"} {
		in := Schedule{ScheduleID: fmt.Sprintf("filter-%d", i), Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "Europe/Moscow", Expression: "0 10 * * 5", Input: "fixture", JobKind: kind}
		if _, err := g.spool.CreateSchedule(in, env, ""); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := g.spool.PauseSchedule(in.ScheduleID, env); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/routines?limit=20&kind=reminder&state=paused", nil)
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var got struct {
		Items []struct {
			ID       string `json:"schedule_id"`
			State    string `json:"state"`
			Kind     string `json:"job_kind"`
			Timezone string `json:"timezone"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "filter-0" || got.Items[0].State != "paused" || got.Items[0].Kind != "reminder" || got.Items[0].Timezone != "Europe/Moscow" {
		t.Fatalf("wrong filtered summary: %s", w.Body.String())
	}
	card, err := g.spool.GetRoutine("filter-0", env)
	if err != nil || card.State != "paused" {
		t.Fatalf("card disagrees: %+v %v", card, err)
	}
}

// A watcher whose probe is dead looks identical to a healthy one in the
// compact list unless the summary carries the durable probe error.
func TestRoutineSummarySurfacesWatchError(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	in := Schedule{ScheduleID: "w-dead", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:5m", Input: "watch dzen", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com"}}
	if _, err := g.spool.CreateSchedule(in, env, ""); err != nil {
		t.Fatal(err)
	}
	st := WatchState{ScheduleID: "w-dead", LastError: "probe status 302"}
	body, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(g.spool.root, "watchers"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.spool.root, "watchers", "w-dead.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/routines?limit=20&kind=watcher", nil)
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	var got struct {
		Items []struct {
			ID        string `json:"schedule_id"`
			LastError string `json:"last_error"`
		} `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "w-dead" || got.Items[0].LastError != "probe status 302" {
		t.Fatalf("summary lost the probe error: %s", w.Body.String())
	}
}

func TestRoutinePreviewUsesActualSnoozedDueWithoutWriting(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	in := Schedule{ScheduleID: "preview", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder"}
	in, err = g.spool.createScheduleAt(in, env, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SnoozeSchedule(in.ScheduleID, env, now.Add(35*time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(g.spool.schedulePath(in.ScheduleID))
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/routines/preview/preview?limit=3", nil)
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Times    []time.Time `json:"times"`
		Timezone string      `json:"timezone"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []time.Time{now.Add(35 * time.Minute), now.Add(45 * time.Minute), now.Add(time.Hour)}
	if !reflect.DeepEqual(got.Times, want) || got.Timezone != "UTC" {
		t.Fatalf("preview=%+v want %v", got, want)
	}
	after, err := os.ReadFile(g.spool.schedulePath(in.ScheduleID))
	if err != nil || string(after) != string(before) {
		t.Fatal("preview changed durable state")
	}
}

func TestRoutineSnoozeRejectsDeadlineBeforeNextRun(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	env := c.Users[0].Envelope(11)
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	g.now = func() time.Time { return now }
	in := Schedule{ScheduleID: "snooze-early", Envelope: env, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder"}
	in, err = g.spool.createScheduleAt(in, env, "", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/routines/snooze-early/snooze", strings.NewReader(`{"until":"2100-01-01T10:10:00Z","expected_revision":1}`))
	request.Header.Set("Authorization", "Bearer control-token")
	request.Header.Set("X-Hub-Principal", "alice")
	response := httptest.NewRecorder()
	g.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "later than the next run") {
		t.Fatalf("snooze before next run: %d %s", response.Code, response.Body.String())
	}
	card, err := g.spool.GetRoutine(in.ScheduleID, env)
	if err != nil || card.Schedule.Revision != in.Revision || !card.Schedule.NextDue.Equal(in.NextDue) || !card.Schedule.SnoozedUntil.IsZero() {
		t.Fatalf("rejected snooze changed schedule: %+v %v", card, err)
	}
}

func TestRoutinePreviewRejectsUnboundedCount(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []string{"0", "-1", "21", "bad"} {
		r := httptest.NewRequest(http.MethodGet, "/v1/routines/fixture/preview?limit="+limit, nil)
		r.Header.Set("Authorization", "Bearer control-token")
		r.Header.Set("X-Hub-Principal", "alice")
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		if w.Code != 400 {
			t.Errorf("preview limit %s: %d", limit, w.Code)
		}
	}
}

func TestRoutinePreviewStopsAtCompletedSchedule(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	in := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", 0)
	preview, err := s.PreviewRoutine(in.ScheduleID, in.Envelope, 20, in.NextDue.Add(-time.Hour))
	if err != nil || len(preview.Times) != 1 || !preview.Times[0].Equal(in.NextDue) {
		t.Errorf("once preview: %+v %v", preview, err)
	}
	if err := s.TickSchedules(in.NextDue, "", nil); err != nil {
		t.Fatal(err)
	}
	card, err := s.GetRoutine(in.ScheduleID, in.Envelope)
	if err != nil || card.State != "completed" {
		t.Errorf("completed state: %+v %v", card, err)
	}
	preview, err = s.PreviewRoutine(in.ScheduleID, in.Envelope, 20, in.NextDue)
	if err != nil || len(preview.Times) != 0 {
		t.Errorf("completed preview: %+v %v", preview, err)
	}
}

func TestRoutineVersionedEditsPreserveLifecycle(t *testing.T) {
	for _, scenario := range []string{"stale", "read-only", "snooze-edit", "paused-text", "kind", "timezone", "resume"} {
		t.Run(scenario, func(t *testing.T) {
			c := testConfig(t)
			c.ControlAuth = "control-token"
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			call := func(path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer control-token")
				r.Header.Set("X-Hub-Principal", "alice")
				w := httptest.NewRecorder()
				g.Handler().ServeHTTP(w, r)
				return w
			}
			w := call("/v1/routines", `{"schedule_id":"lifecycle","timezone":"UTC","expression":"0 10 * * 5","input":"fixture","job_kind":"reminder"}`)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			var original Schedule
			if err := json.Unmarshal(w.Body.Bytes(), &original); err != nil {
				t.Fatal(err)
			}
			rev := original.Revision
			action := ""
			patch := `"input":"edited"`
			switch scenario {
			case "stale":
				rev = 99
			case "read-only":
				patch = `"chat_id":999`
			case "snooze-edit", "paused-text":
				until := time.Now().UTC().Add(30 * 24 * time.Hour)
				if err := g.spool.SnoozeSchedule(original.ScheduleID, original.Envelope, until); err != nil {
					t.Fatal(err)
				}
				rev++
				if scenario == "paused-text" {
					if err := g.spool.PauseSchedule(original.ScheduleID, original.Envelope); err != nil {
						t.Fatal(err)
					}
					rev++
					original.NextDue = until
					original.SnoozedUntil = until
				} else {
					patch = `"expression":"0 11 * * 5"`
				}
			case "kind":
				patch = `"job_kind":"script","input":"print(1)"`
			case "timezone":
				patch = `"timezone":"Europe/Moscow"`
			case "resume":
				if err := g.spool.PauseSchedule(original.ScheduleID, original.Envelope); err != nil {
					t.Fatal(err)
				}
				rev++
				action = "/resume"
			}
			w = call("/v1/routines/lifecycle"+action, fmt.Sprintf(`{"expected_revision":%d,%s}`, rev, patch))
			if scenario == "stale" {
				if w.Code != 409 {
					t.Fatalf("stale write: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if scenario == "read-only" {
				if w.Code != 400 {
					t.Fatalf("read-only field: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("edit: %d %s", w.Code, w.Body.String())
			}
			items, err := g.spool.ListSchedules(original.Envelope)
			if err != nil || len(items) != 1 {
				t.Fatalf("read: %+v %v", items, err)
			}
			got := items[0]
			if got.Revision != rev+1 {
				t.Errorf("revision=%d want %d", got.Revision, rev+1)
			}
			switch scenario {
			case "snooze-edit":
				if !got.SnoozedUntil.IsZero() {
					t.Fatal("timing edit retained obsolete snooze")
				}
			case "paused-text":
				if !got.Paused || !got.NextDue.Equal(original.NextDue) || !got.SnoozedUntil.Equal(original.SnoozedUntil) {
					t.Fatal("text edit moved paused/snoozed due")
				}
			case "kind":
				if got.JobKind != "script" {
					t.Fatal("kind not updated")
				}
			case "timezone":
				if got.Timezone != "Europe/Moscow" {
					t.Fatal("timezone not updated")
				}
			case "resume":
				if got.Paused || !got.Enabled {
					t.Fatal("routine not resumed")
				}
			}
		})
	}
}

func TestRoutineIntervalPersistsAnchorAndCompletesAtEnd(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		t.Run(map[bool]string{false: "unbounded", true: "bounded"}[bounded], func(t *testing.T) {
			s, o, _ := occurrenceFixture(t)
			now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
			in := Schedule{ScheduleID: "interval-restart", Envelope: o.Job.Envelope, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "every:15m", Input: "fixture", JobKind: "reminder"}
			if bounded {
				in.Timing = &RoutineTiming{EndAt: now.Add(30 * time.Minute)}
			}
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("optional timing must not panic: %v", r)
				}
			}()
			got, err := s.createScheduleAt(in, in.Envelope, "", nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if !got.IntervalAnchor.Equal(now) {
				t.Errorf("anchor not persisted: %s", got.IntervalAnchor)
			}
			restart, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			if err := restart.TickSchedules(now.Add(15*time.Minute), "", nil); err != nil {
				t.Fatalf("last slot must settle: %v", err)
			}
			items, err := restart.ListSchedules(in.Envelope)
			if err != nil || len(items) != 1 {
				t.Fatalf("read: %v %v", items, err)
			}
			if bounded {
				if items[0].Enabled || !items[0].NextDue.IsZero() {
					t.Fatalf("exhausted interval not completed: %+v", items[0])
				}
				return
			}
			next, err := nextSchedule(items[0], time.UTC, now.Add(22*time.Minute))
			if err != nil || !next.Equal(now.Add(30*time.Minute)) {
				t.Fatalf("restart cadence drift: %s %v", next, err)
			}
			_, err = nextSchedule(Schedule{Expression: "every:15m", Timing: &RoutineTiming{EndAt: now}}, time.UTC, now)
			if !errors.Is(err, errRoutineExhausted) {
				t.Fatal("exhaustion must be explicit")
			}
		})
	}
}

func TestRoutineTimingRejectsUnsafeConfiguration(t *testing.T) {
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, expression string
		timing           *RoutineTiming
		delivery         string
	}{
		{"too fast", "every:1s", &RoutineTiming{}, ""},
		{"zero", "every:0s", &RoutineTiming{}, ""},
		{"negative", "every:-1m", &RoutineTiming{}, ""},
		{"too long", "every:9000h", &RoutineTiming{}, ""},
		{"fractional", "every:60.5s", &RoutineTiming{}, ""},
		{"equal window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{From: "14:00", To: "14:00"}}}, ""},
		{"bad window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{From: "25:00", To: "16:00"}}}, ""},
		{"many windows", "every:1m", &RoutineTiming{Windows: make([]RoutineWindow, 9)}, ""},
		{"many times", "every:1m", &RoutineTiming{At: make([]string, 25)}, ""},
		{"bad time", "every:1m", &RoutineTiming{At: []string{"25:00"}}, ""},
		{"duplicate time", "every:1m", &RoutineTiming{At: []string{"17:00", "17:00"}}, ""},
		{"duplicate window", "every:1m", &RoutineTiming{Windows: []RoutineWindow{{From: "14:00", To: "16:00"}, {From: "14:00", To: "16:00"}}}, ""},
		{"cron timing", "0 10 * * *", &RoutineTiming{At: []string{"17:00"}}, ""},
		{"stream timing", "every:1m", &RoutineTiming{At: []string{"17:00"}}, "stream"},
		{"stream interval", "every:1m", nil, "stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Zero and negative durations must be rejected before any division.
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("unsafe timing panicked: %v", r)
				}
			}()
			_, err := nextSchedule(Schedule{Expression: tc.expression, Timing: tc.timing, Delivery: tc.delivery}, time.UTC, now)
			if err == nil {
				t.Fatal("unsafe timing accepted")
			}
		})
	}
}

func TestRoutineIntervalCalendarSlots(t *testing.T) {
	for _, tc := range []struct{ name, zone, from, config, want string }{
		{"anchored", "UTC", "2100-01-01T10:07:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{}}`, "2100-01-01T10:15:00Z"},
		{"overnight opening weekday", "UTC", "2100-01-02T00:05:00Z", `{"expression":"every:30m","timing":{"weekdays":[5],"windows":[{"from":"23:00","to":"01:00"}]}}`, "2100-01-02T00:30:00Z"},
		{"next selected day", "UTC", "2100-01-02T01:00:00Z", `{"expression":"every:30m","timing":{"weekdays":[5],"windows":[{"from":"23:00","to":"01:00"}]}}`, "2100-01-08T23:00:00Z"},
		{"start inclusive", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{"start_at":"2100-01-01T11:00:00Z","end_at":"2100-01-01T12:00:00Z"}}`, "2100-01-01T11:00:00Z"},
		{"end exclusive", "UTC", "2100-01-01T11:45:00Z", `{"expression":"every:15m","interval_anchor":"2100-01-01T10:00:00Z","timing":{"end_at":"2100-01-01T12:00:00Z"}}`, ""},
		{"invalid weekday", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"weekdays":[7]}}`, ""},
		{"duplicate weekday", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"weekdays":[5,5]}}`, ""},
		{"invalid bounds", "UTC", "2100-01-01T10:00:00Z", `{"expression":"every:15m","timing":{"start_at":"2100-01-02T10:00:00Z","end_at":"2100-01-01T10:00:00Z"}}`, ""},
		{"DST gap", "America/New_York", "2026-03-08T06:59:00Z", `{"expression":"every:30m","timing":{"windows":[{"from":"02:00","to":"04:00"}]}}`, "2026-03-08T07:00:00Z"},
		{"DST fold extra", "America/New_York", "2026-11-01T05:45:00Z", `{"expression":"every:2h","timing":{"windows":[{"from":"12:00","to":"13:00"}],"at":["01:30"]}}`, "2026-11-01T06:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("calendar evaluation panicked: %v", r)
				}
			}()
			var in Schedule
			if err := json.Unmarshal([]byte(tc.config), &in); err != nil {
				t.Fatal(err)
			}
			from, err := time.Parse(time.RFC3339, tc.from)
			if err != nil {
				t.Fatal(err)
			}
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := nextSchedule(in, loc, from)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("invalid/exhausted calendar returned %s", got)
				}
				return
			}
			if err != nil || got.Format(time.RFC3339) != tc.want {
				t.Fatalf("slot = %s %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestRoutineIntervalWindowCadence(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2100, 1, 1, 10, 0, 0, 0, time.UTC)
	var in Schedule
	if err := json.Unmarshal([]byte(`{"schedule_id":"window-test","timezone":"Europe/Moscow","expression":"every:15m","input":"fixture","job_kind":"reminder","channel":"telegram_bot","chat_id":11,"timing":{"windows":[{"from":"14:00","to":"16:00"}],"at":["17:00"]}}`), &in); err != nil {
		t.Fatal(err)
	}
	in.Envelope = c.Users[0].Envelope(11)
	in.UserID, in.ActorID, in.ScopeID = "alice", "alice", "user:alice"
	got, err := g.spool.createScheduleAt(in, in.Envelope, "", nil, now)
	if err != nil {
		t.Fatalf("interval window creation: %v", err)
	}
	want := now.Add(time.Hour)
	if !got.NextDue.Equal(want) {
		t.Fatalf("first window slot = %s, want %s", got.NextDue, want)
	}
	for i := 0; i < 9; i++ {
		due := want.Add(time.Duration(i) * 15 * time.Minute)
		if i == 8 {
			due = want.Add(3 * time.Hour)
		}
		items, err := g.spool.ListSchedules(in.Envelope)
		if err != nil || len(items) != 1 || !items[0].NextDue.Equal(due) {
			t.Fatalf("slot %d: %+v %v; want %s", i, items, err, due)
		}
		if err := g.spool.TickSchedules(due, "", nil); err != nil {
			t.Fatal(err)
		}
		if err := g.spool.DispatchDueOccurrences(due, func(Job) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}
