package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/commodel"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func batchFixture(t *testing.T) (*Spool, Schedule, *Gateway) {
	t.Helper()
	s, o, g := occurrenceFixture(t)
	sched, err := s.CreateSchedule(Schedule{ScheduleID: "durable-batch", Envelope: o.Job.Envelope, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "fixture batch", JobKind: "event", Delivery: "stream", Probe: &WatchProbe{Kind: "mcp_tool", Tool: "hub-watch-chat-messages", CursorArg: "cursor", MinIntervalSeconds: 60}}, o.Job.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, sched, g
}

func tenBatch(t *testing.T) []byte {
	t.Helper()
	var events []map[string]string
	for i := 0; i < 10; i++ {
		events = append(events, map[string]string{"id": fmt.Sprint(i), "text": fmt.Sprintf("event %d", i)})
	}
	body, err := json.Marshal(map[string]any{"events": events, "next_cursor": "10"})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestBatchTenEventsEightSlotsRestartRemainder(t *testing.T) {
	s, sched, g := batchFixture(t)
	calls := 0
	source := func(context.Context, string, *WatchProbe) ([]byte, error) {
		calls++
		if calls == 1 {
			return tenBatch(t), nil
		}
		return []byte(`{"events":[],"next_cursor":"20"}`), nil
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, source); blocked {
		t.Fatal("first batch blocked")
	}
	st, err := s.EventStateOf(sched.ScheduleID)
	if err != nil || len(st.Seen) != 8 || st.Cursor != "" {
		t.Fatal("first admission", err)
	}
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = reopened
	if _, blocked := g.streamPollOnce(context.Background(), sched, source); blocked {
		t.Fatal("replay blocked")
	}
	st, err = reopened.EventStateOf(sched.ScheduleID)
	if err != nil || len(st.Seen) != 10 || st.Cursor != "10" || calls != 1 {
		t.Fatalf("lost saved remainder or probed before recovery: seen=%d cursor=%q calls=%d err=%v", len(st.Seen), st.Cursor, calls, err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if err != nil || len(entries) != 10 {
		t.Fatal("wrong deterministic sink count", len(entries), err)
	}
}

func TestBatchIntentWriteFailureDoesNotAdvanceCursor(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Probe.Filter = `emit("keep")`
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	if err := s.writeEventStateLocked(sched.ScheduleID, EventState{ScheduleID: sched.ScheduleID, ProbeRevision: sched.ProbeRevision}); err != nil {
		t.Fatal(err)
	}
	injected := false
	s.probeScript = func(context.Context, RoutineOccurrence, string, string, bool, []string) (string, error) {
		if !injected {
			injected = true
			marker := s.eventPath(sched.ScheduleID) + ".initialized"
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(marker, 0700); err != nil {
				t.Fatal(err)
			}
		}
		return "keep", nil
	}
	source := func(context.Context, string, *WatchProbe) ([]byte, error) { return tenBatch(t), nil }
	_, blocked := g.streamPollOnce(context.Background(), sched, source)
	entries, err := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if !blocked || len(entries) != 0 {
		t.Fatalf("fanout ran before durable intent: blocked=%t deliveries=%d", blocked, len(entries))
	}
	st, _ := s.EventStateOf(sched.ScheduleID)
	if st.Cursor != "" || len(st.Seen) != 0 {
		t.Fatal("failed intent advanced state")
	}
}

func TestPauseDuringBatchPreventsEffect(t *testing.T) {
	s, sched, g := batchFixture(t)
	source := func(context.Context, string, *WatchProbe) ([]byte, error) { return tenBatch(t), nil }
	g.streamPollOnce(context.Background(), sched, source)
	s.mu.Lock()
	live, err := s.loadScheduleLocked(sched.ScheduleID)
	if err == nil {
		live.Paused = true
		err = atomicJSON(filepath.Join(s.root, "schedules", spoolFileID(sched.ScheduleID)+".json"), live)
	}
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	_, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
		t.Fatal("paused batch fetched source")
		return nil, nil
	})
	entries, _ := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if !blocked || len(entries) != 8 {
		t.Fatal("paused replay admitted an effect")
	}
}

func TestBatchAgentRemainderKeepsExactPayloadAndRevision(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Sink = "agent"
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	var events []map[string]string
	for i := 0; i < 18; i++ {
		events = append(events, map[string]string{"id": fmt.Sprint(i), "text": strings.Repeat("x", 4000)})
	}
	body, _ := json.Marshal(map[string]any{"events": events, "next_cursor": "18"})
	if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) { return body, nil }); blocked {
		t.Fatal("initial agent batch blocked")
	}
	st, err := s.EventStateOf(sched.ScheduleID)
	if err != nil || st.PendingBatch == nil || len(st.PendingBatch.Jobs) != 3 {
		t.Fatal("agent batch did not persist exact groups", err)
	}
	planned := st.PendingBatch.Jobs
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = reopened
	for i := 0; i < 2; i++ {
		if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
			t.Fatal("agent recovery called source")
			return nil, nil
		}); blocked {
			t.Fatal("agent recovery blocked")
		}
	}
	st, err = reopened.EventStateOf(sched.ScheduleID)
	if err != nil || st.PendingBatch != nil || st.Cursor != "18" || len(st.Seen) != 18 {
		t.Fatal("agent remainder lost", err)
	}
	for id, expected := range planned {
		raw, err := os.ReadFile(filepath.Join(s.root, "pending", spoolFileID(id)+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var got Job
		if json.Unmarshal(raw, &got) != nil || got.Text != expected.Text || got.ID != expected.ID || !got.CreatedAt.Equal(expected.CreatedAt) {
			t.Fatal("replay changed prepared agent payload")
		}
		g.spool.mu.Lock()
		ok := g.occurrenceRefAuthorizedLocked(id)
		g.spool.mu.Unlock()
		if !ok {
			t.Fatal("current batch reference rejected")
		}
	}
	sched.ProbeRevision++
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	for id := range planned {
		g.spool.mu.Lock()
		ok := g.occurrenceRefAuthorizedLocked(id)
		g.spool.mu.Unlock()
		if ok {
			t.Fatal("stale probe revision authorized delivery")
		}
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job != nil {
		t.Fatal("stale batch reached runner", err)
	}
}

func TestBatchDroppedEventsDoNotDependOnSeenWindow(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Probe.Filter = `emit("drop")`
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	s.probeScript = func(context.Context, RoutineOccurrence, string, string, bool, []string) (string, error) {
		return "drop", nil
	}
	var events []map[string]string
	for i := 0; i < 600; i++ {
		events = append(events, map[string]string{"id": fmt.Sprint(i), "text": "fixture"})
	}
	body, _ := json.Marshal(map[string]any{"events": events, "next_cursor": "600"})
	if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) { return body, nil }); blocked {
		t.Fatal("drop batch blocked")
	}
	st, err := s.EventStateOf(sched.ScheduleID)
	if err != nil || len(st.Seen) != 512 || st.PendingBatch != nil || st.Cursor != "600" || st.GapCount != 88 {
		t.Fatal("seen eviction lost durable batch decisions", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if len(entries) != 0 {
		t.Fatal("drop created sink effects")
	}
}

func TestBatchOccurrenceRestartDoesNotFetchAgain(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Delivery = "poll"
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o := RoutineOccurrence{ScheduleID: sched.ScheduleID, Revision: sched.Revision, DueAt: now.Add(-time.Minute), JobKind: "event", Job: Job{Envelope: sched.Envelope, OrganizationID: sched.OrganizationID, UserID: sched.UserID, ActorID: sched.ActorID, ScopeID: sched.ScopeID, Channel: sched.Channel, ChatID: sched.ChatID, Text: sched.Input}}
	if err := s.PutOccurrence(o, sched.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	calls := 0
	source := func(context.Context, string, *WatchProbe) ([]byte, error) { calls++; return tenBatch(t), nil }
	if err := s.ProbeDueOccurrencesContext(context.Background(), now, g.authorizeOccurrence, nil, source, nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = reopened
	if err := reopened.ProbeDueOccurrencesContext(context.Background(), now, g.authorizeOccurrence, nil, source, nil); err != nil {
		t.Fatal(err)
	}
	st, err := reopened.EventStateOf(sched.ScheduleID)
	if err != nil || st.PendingBatch != nil || st.Cursor != "10" || len(st.Seen) != 10 || calls != 1 {
		t.Fatalf("poll remainder lost or fetched again: cursor=%q seen=%d calls=%d err=%v", st.Cursor, len(st.Seen), calls, err)
	}
}

func TestBatchRepairsExactPendingAdmissionOnly(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Sink = "agent"
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: sched.Envelope, ID: "source-fixture", IdempotencyKey: "source-fixture", OrganizationID: sched.OrganizationID, UserID: sched.UserID, ActorID: sched.ActorID, ScopeID: sched.ScopeID, Channel: sched.Channel, ChatID: sched.ChatID, Trigger: "cron", Text: sched.Input, CreatedAt: time.Now().UTC()}
	st, err := s.EventStateOf(sched.ScheduleID)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{{ID: "fixture-event", Text: "fixture"}}
	s.mu.Lock()
	err = s.startSourceBatchLocked(&st, sched, job, events, nil, job.ID, "1", nil, map[string][]string{"": eventIDs(events)}, nil, job.CreatedAt)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var planned Job
	for _, j := range st.PendingBatch.Jobs {
		planned = j
	}
	if planned.ID == "" {
		t.Fatal("planned sink missing")
	}
	if err := atomicJSON(filepath.Join(s.root, "pending", spoolFileID(planned.ID)+".json"), planned); err != nil {
		t.Fatal(err)
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
		t.Fatal("repair fetched source")
		return nil, nil
	}); blocked {
		t.Fatal("exact pending derivative not repaired")
	}
	m, found, err := s.Mapping(planned.ID)
	if err != nil || !found || !m.MatchesJob(planned) {
		t.Fatal("mapping not restored from saved intent", err)
	}
	// A changed pending file cannot inherit that intent's authorization.
	if err := os.Remove(s.mappingPath(planned.ID)); err != nil {
		t.Fatal(err)
	}
	planned.Envelope.ContextID = "foreign"
	if err := atomicJSON(filepath.Join(s.root, "pending", spoolFileID(planned.ID)+".json"), planned); err != nil {
		t.Fatal(err)
	}
	original := st.PendingBatch.Jobs[planned.ID]
	s.mu.Lock()
	_, err = s.jobAdmissionLocked(original)
	s.mu.Unlock()
	if err == nil {
		t.Fatal("foreign pending job repaired as admitted")
	}
}

func TestBatchAssignmentWakeReservationSurvivesEnqueueFailure(t *testing.T) {
	s, sched, g := batchFixture(t)
	sched.Sink = "agent"
	sched.AssignmentID = "batch-assignment"
	sched.AssignmentPlanRevision = 1
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := commodel.Assignment{AssignmentID: sched.AssignmentID, Revision: 1, Envelope: sched.Envelope, Intent: "fixture", State: "active", Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1, Grants: commodel.AssignmentGrants{MaxWakesPerDay: 1}, CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(filepath.Join(s.root, "assignments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.rt.AssignmentPath(a.AssignmentID), a); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, "pending")
	if err := os.Rename(pending, pending+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("fixture fault"), 0600); err != nil {
		t.Fatal(err)
	}
	g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
		return []byte(`{"events":[{"id":"one","text":"fixture"}],"next_cursor":"1"}`), nil
	})
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending+".saved", pending); err != nil {
		t.Fatal(err)
	}
	if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
		t.Fatal("reserved wake refetched source")
		return nil, nil
	}); blocked {
		t.Fatal("reserved wake blocked")
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("enqueue failure spent the only wake and stranded its saved operation", err)
	}
	body, err := os.ReadFile(s.rt.AssignmentPath(a.AssignmentID))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(body, &a) != nil || a.Checkpoint.WakesToday != 1 || a.Checkpoint.LLMCalls != 1 {
		t.Fatal("recovery charged a second wake")
	}
}

func TestCorruptBatchCannotAdvanceCursorOrFetchAnotherSource(t *testing.T) {
	for _, kind := range []string{"missing_decisions", "missing_source_interval"} {
		t.Run(kind, func(t *testing.T) {
			s, sched, g := batchFixture(t)
			g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) { return tenBatch(t), nil })
			st, err := s.EventStateOf(sched.ScheduleID)
			if err != nil || st.PendingBatch == nil {
				t.Fatal("missing pending fixture", err)
			}
			if kind == "missing_decisions" {
				st.PendingBatch.Settled = nil
			} else {
				st.PendingBatch.SourceEvents = nil
			}
			if err := atomicJSON(s.eventPath(sched.ScheduleID), st); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			g.spool = reopened
			if _, blocked := g.streamPollOnce(context.Background(), sched, func(context.Context, string, *WatchProbe) ([]byte, error) {
				t.Fatal("corrupt batch fetched another source")
				return nil, nil
			}); !blocked {
				t.Fatal("corrupt batch not blocked")
			}
			current, err := reopened.EventStateOf(sched.ScheduleID)
			if err != nil || current.Cursor != "" {
				t.Fatal("corrupt batch advanced source cursor", err)
			}
			entries, _ := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
			if len(entries) != 8 {
				t.Fatal("corrupt batch admitted a new effect")
			}
		})
	}
}

func TestWatcherWakeEnqueueFailureReplaysSavedIntent(t *testing.T) {
	s, sched, g := batchFixture(t)
	now := time.Now().UTC()
	sched.JobKind = "watcher"
	sched.Delivery = "poll"
	sched.Sink = ""
	sched.NotifyMode = "agent"
	sched.Probe = &WatchProbe{Kind: "http", URL: "https://example.invalid/source"}
	sched.AssignmentID = "watcher-assignment"
	sched.AssignmentPlanRevision = 1
	if err := atomicJSON(s.schedulePath(sched.ScheduleID), sched); err != nil {
		t.Fatal(err)
	}
	a := commodel.Assignment{AssignmentID: sched.AssignmentID, Revision: 1, Envelope: sched.Envelope, Intent: "fixture", State: "active", Plan: &commodel.AssignmentPlan{Revision: 1}, AppliedRev: 1, Grants: commodel.AssignmentGrants{MaxWakesPerDay: 1}, CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(filepath.Join(s.root, "assignments"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(s.rt.AssignmentPath(a.AssignmentID), a); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWatchBaseline(sched.ScheduleID, "v1", now); err != nil {
		t.Fatal(err)
	}
	o := RoutineOccurrence{ScheduleID: sched.ScheduleID, Revision: sched.Revision, DueAt: now.Add(-time.Minute), JobKind: "watcher", Job: Job{Envelope: sched.Envelope, OrganizationID: sched.OrganizationID, UserID: sched.UserID, ActorID: sched.ActorID, ScopeID: sched.ScopeID, Channel: sched.Channel, ChatID: sched.ChatID, Text: sched.Input, AssignmentID: sched.AssignmentID, AssignmentPlanRevision: 1}}
	if err := s.PutOccurrence(o, sched.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(s.root, "pending")
	if err := os.Rename(pending, pending+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("fixture fault"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	fetch := func(context.Context, string) ([]byte, error) { calls++; return []byte("v2"), nil }
	if err := s.ProbeDueOccurrencesContext(context.Background(), now, g.authorizeOccurrence, fetch, nil, nil); err == nil {
		t.Error("watcher enqueue failure was hidden")
	}
	receipt := filepath.Join(s.root, "occurrences", occurrenceID(o)+".probe-result")
	if _, err := os.Stat(receipt); err != nil {
		t.Error("watcher had no durable job intent before enqueue")
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending+".saved", pending); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrencesContext(context.Background(), now, g.authorizeOccurrence, fetch, nil, nil); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("saved watcher wake was lost after enqueue failure", err)
	}
	if calls != 1 {
		t.Fatal("watcher replay fetched source again")
	}
	body, err := os.ReadFile(s.rt.AssignmentPath(a.AssignmentID))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(body, &a) != nil || a.Checkpoint.WakesToday != 1 || a.Checkpoint.LLMCalls != 1 {
		t.Fatal("watcher replay charged another wake")
	}
}
