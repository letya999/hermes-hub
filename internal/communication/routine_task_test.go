package communication

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/routines"
	"time"
)

func TestNamedTaskRoutineSurvivesAdmissionAndRejectsForeignConversation(t *testing.T) {
	s, _, _ := occurrenceFixture(t)
	task, _, err := s.CreateTask(User{ID: "alice", Enabled: true}, 11, 0, "routine fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	u := s.g.config.Users[0]
	job := Job{Envelope: u.Envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, Trigger: "cron", Text: "synthetic"}
	occ := RoutineOccurrence{ScheduleID: "routine-fixture", Revision: 1, DueAt: time.Now().UTC(), Job: job}
	if err := s.PutOccurrence(occ, job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(occ.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatal(err)
	}
	if claimed.ConversationID != routines.RoutineConversation(occ.ScheduleID) {
		t.Fatalf("routine session not isolated: %q", claimed.ConversationID)
	}
	if claimed.TaskID != task.TaskID || claimed.TopicID != task.TopicID {
		t.Fatal("task delivery binding lost")
	}
	if err := s.DispatchDueOccurrences(occ.DueAt.Add(time.Second), s.authorizeOccurrence); err != nil {
		t.Fatal("second tick stalled after task admission", err)
	}
	// The durable fence is the admission authorization the production host
	// runs (ChannelPolicy.Authorize): a mutated conversation, task binding,
	// target or principal can never re-ride the recorded bindings.
	for _, mutate := range []func(*Job){func(j *Job) { j.ConversationID = "foreign-conversation" }, func(j *Job) { j.TaskID = "foreign-task" }, func(j *Job) { j.ChatID = 22 }, func(j *Job) { j.TopicID = 7 }, func(j *Job) { j.PrincipalID = "bob" }} {
		forged := *claimed
		mutate(&forged)
		if err := s.policy.Authorize(context.Background(), occ.ScheduleID, forged); err == nil {
			t.Fatal("forged task binding accepted", forged)
		}
	}
}
