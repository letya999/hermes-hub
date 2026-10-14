package communication

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Routine jobs must not share the user's interactive session: an occurrence
// admitted for a named task pins a schedule-derived conversation, while
// TaskID/TopicID still route deliveries back to the task topic.
func TestOccurrenceUsesScheduleConversation(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	u := c.Users[0]
	task, _, err := g.spool.CreateTask(u, 11, 0, "routine fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: u.envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, Trigger: "cron", Text: "synthetic"}
	occ := RoutineOccurrence{ScheduleID: "routine-fixture", Revision: 1, DueAt: time.Now().UTC(), Job: job}
	if err := g.spool.PutOccurrence(occ, job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.DispatchDueOccurrences(occ.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	claimed, err := g.spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if claimed.ConversationID != "routine-routine-fixture" {
		t.Fatalf("routine shares interactive conversation: %q", claimed.ConversationID)
	}
	if claimed.TaskID != task.TaskID {
		t.Fatalf("delivery routing lost task binding: %q", claimed.TaskID)
	}
	if err := g.authorizeOccurrence(*claimed); err != nil {
		t.Fatal("admitted routine conversation rejected", err)
	}
	mapping, found, err := g.spool.Mapping(claimed.ID)
	if err != nil || !found {
		t.Fatal("admitted routine lost its mapping", err)
	}
	if mapping.ConversationID != "routine-routine-fixture" {
		t.Fatalf("mapping kept old conversation: %q", mapping.ConversationID)
	}
	if err := g.spool.DispatchDueOccurrences(occ.DueAt.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal("second tick broke on routine conversation", err)
	}
}

// An occurrence admitted before schedule-derived conversations existed keeps
// its task-pinned pending job and mapping; both claim-time authorization and
// the dispatch binding check must still accept it so the run can settle.
func TestInFlightOccurrenceWithLegacyConversationSettles(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	u := c.Users[0]
	task, _, err := g.spool.CreateTask(u, 11, 0, "legacy routine", false)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: u.envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, Trigger: "cron", Text: "synthetic"}
	occ := RoutineOccurrence{ScheduleID: "legacy-fixture", Revision: 1, DueAt: time.Now().UTC(), Job: job}
	if err := g.spool.PutOccurrence(occ, job.Envelope); err != nil {
		t.Fatal(err)
	}
	occ.Job.ID = OccurrenceID(occ)
	if err := g.spool.DispatchDueOccurrences(occ.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	// Rewrite the admitted job and mapping to the legacy task-pinned form,
	// as pre-change code would have written them.
	pending := filepath.Join(g.spool.root, "pending", spoolFileID(occ.Job.ID)+".json")
	body, err := os.ReadFile(pending)
	if err != nil {
		t.Fatal(err)
	}
	var legacy Job
	if err := json.Unmarshal(body, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Envelope.ConversationID = task.ConversationID
	if err := atomicJSON(pending, legacy); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := g.spool.Mapping(occ.Job.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	mapping.ConversationID = task.ConversationID
	mapping.Fingerprint = jobFingerprint(legacy)
	if err := atomicJSON(filepath.Join(g.spool.root, "mappings", spoolFileID(occ.Job.ID)+".json"), mapping); err != nil {
		t.Fatal(err)
	}
	claimed, err := g.spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if claimed.ConversationID != task.ConversationID {
		t.Fatalf("legacy pending job lost its conversation: %q", claimed.ConversationID)
	}
	if err := g.authorizeOccurrence(*claimed); err != nil {
		t.Fatal("legacy in-flight occurrence rejected after upgrade", err)
	}
	if err := g.spool.DispatchDueOccurrences(occ.DueAt.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal("dispatch binding check rejected legacy conversation", err)
	}
}

type routineFailRunner struct{ fail bool }

func (r routineFailRunner) Run(_ context.Context, job Job, _ User) (string, error) {
	if r.fail {
		return "", errors.New("hermes unavailable")
	}
	return "ok", nil
}

// A routine that keeps failing must not spam the owner: identical failures
// dedupe to one delivery; a success resets the digest so a later failure
// notifies again.
func TestRoutineRepeatedFailureDeliveredOnce(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	u := c.Users[0]
	caller := u.envelope(11)
	if _, err := g.spool.CreateSchedule(Schedule{ScheduleID: "failing-routine", Envelope: caller, OrganizationID: c.OrganizationID, UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "fails every tick", JobKind: "agent"}, caller, ""); err != nil {
		t.Fatal(err)
	}
	sched, err := g.spool.loadScheduleLocked("failing-routine")
	if err != nil {
		t.Fatal(err)
	}
	base := Job{Envelope: caller, UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, Trigger: "cron", Text: "fails every tick"}
	fire := func(seq int) string {
		occ := RoutineOccurrence{ScheduleID: sched.ScheduleID, Revision: sched.Revision, DueAt: time.Unix(1000, 0).Add(time.Duration(seq) * time.Minute), Job: base}
		if err := g.spool.PutOccurrence(occ, caller); err != nil {
			t.Fatal(err)
		}
		if err := g.spool.DispatchDueOccurrences(occ.DueAt.Add(time.Second), g.authorizeOccurrence); err != nil {
			t.Fatal(err)
		}
		return OccurrenceID(occ)
	}
	failures := func() int {
		n := 0
		for _, text := range fake.sent {
			if strings.Contains(text, "Не удалось завершить задачу") {
				n++
			}
		}
		// Drain deliveries enqueued but not yet sent by deliverOne.
		for {
			d, err := g.spool.ClaimDelivery()
			if err != nil || d == nil {
				break
			}
			if strings.Contains(d.Text, "Не удалось завершить задачу") {
				n++
			}
			_ = g.spool.CompleteDelivery(d.ID)
		}
		return n
	}
	settled := func(jobID string) bool {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if m, found, _ := g.spool.Mapping(jobID); found && terminalStatus(m.Status) {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return false
	}
	g.runner = routineFailRunner{fail: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	for i := 0; i < 10; i++ {
		if !settled(fire(i)) {
			t.Fatalf("occurrence %d never settled", i)
		}
	}
	cancel()
	<-done
	if n := failures(); n != 1 {
		t.Fatalf("10 identical failures produced %d deliveries: %v", n, fake.sent)
	}
	// A success resets the digest: the next identical failure notifies again.
	g.runner = routineFailRunner{fail: false}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	if !settled(fire(10)) {
		t.Fatal("successful occurrence never settled")
	}
	cancel()
	<-done
	if s2, err := g.spool.loadScheduleLocked("failing-routine"); err != nil || s2.LastFailureSHA256 != "" {
		t.Fatalf("success did not clear failure digest: %q %v", s2.LastFailureSHA256, err)
	}
	g.runner = routineFailRunner{fail: true}
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	if !settled(fire(11)) {
		t.Fatal("post-success failure never settled")
	}
	cancel()
	<-done
	if n := failures(); n != 2 {
		sched, _ := g.spool.loadScheduleLocked("failing-routine")
		t.Fatalf("failure after success produced %d deliveries: %v digest=%q", n, fake.sent, sched.LastFailureSHA256)
	}
}

// A job that fails must not be logged as completed: operators grep
// `status="completed"` to confirm work landed.
func TestFailedJobLogsFailed(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	u := c.Users[0]
	job := Job{Envelope: u.envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, Text: "boom"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g.runner = routineFailRunner{fail: true}
	var output strings.Builder
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m, found, _ := g.spool.Mapping(job.ID); found && terminalStatus(m.Status) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !strings.Contains(output.String(), `"status":"failed"`) {
		t.Fatalf("failed job did not log failed status:\n%s", output.String())
	}
}

// When an interactive job and a routine occurrence wait on the same context,
// the interactive one is claimed first — a due routine must not block a user
// who is actively typing.
func TestInteractiveJobClaimedBeforeOccurrence(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	// The interactive filename sorts after the occurrence's, so a plain
	// directory-order claim would pick the routine first.
	interactive := Job{Envelope: o.Job.Envelope, ID: "zz-interactive", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "zz-interactive", ChatID: 11, Text: "live question", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(interactive); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		claimed, err := s.ClaimJob()
		if err != nil || claimed == nil {
			t.Fatal(err)
		}
		if i == 0 && claimed.Trigger == "cron" {
			t.Fatalf("occurrence claimed before interactive job: %s", claimed.ID)
		}
		if err := s.FailJob(claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
}
