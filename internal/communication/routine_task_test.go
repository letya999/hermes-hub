package communication

import (
	"testing"
	"time"
)

func TestNamedTaskRoutineSurvivesAdmissionAndRejectsForeignConversation(t *testing.T) {
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
	if err := g.authorizeOccurrence(job); err != nil {
		t.Fatal("pre-admission", err)
	}
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
	if claimed.ConversationID != task.ConversationID {
		t.Fatal("task not bound")
	}
	if err := g.authorizeOccurrence(*claimed); err != nil {
		t.Fatal("owned task rejected after admission", err)
	}
	if err := g.spool.DispatchDueOccurrences(occ.DueAt.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal("second tick stalled after task admission", err)
	}
	for _, mutate := range []func(*Job){func(j *Job) { j.ConversationID = "foreign-conversation" }, func(j *Job) { j.TaskID = "foreign-task" }, func(j *Job) { j.ChatID = 22 }, func(j *Job) { j.TopicID = 7 }, func(j *Job) { j.PrincipalID = "bob" }} {
		forged := *claimed
		mutate(&forged)
		if g.authorizeOccurrence(forged) == nil {
			t.Fatal("forged task binding accepted", forged)
		}
	}
}
