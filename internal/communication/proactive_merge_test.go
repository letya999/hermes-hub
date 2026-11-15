package communication

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Proactive fan-out: a watcher change feeds a digest delivery, a brief
// becomes an agent job with the untrusted snapshot, and an mcp_tool event
// schedule fans discrete events out to the sink exactly once.
func TestDigestBriefAndEventDispatch(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	caller := o.Job.Envelope
	base := Schedule{Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *"}

	watcher := base
	watcher.ScheduleID = "w1"
	watcher.Input = "watch the price"
	watcher.JobKind = "watcher"
	watcher.Probe = &WatchProbe{URL: "https://example.com/p"}
	if _, err := s.CreateSchedule(watcher, caller, ""); err != nil {
		t.Fatal(err)
	}
	digest := base
	digest.ScheduleID = "d1"
	digest.Input = "morning digest"
	digest.JobKind = "digest"
	if _, err := s.CreateSchedule(digest, caller, ""); err != nil {
		t.Fatal(err)
	}
	brief := base
	brief.ScheduleID = "b1"
	brief.Input = "morning brief"
	brief.JobKind = "brief"
	if _, err := s.CreateSchedule(brief, caller, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTask(User{ID: o.Job.UserID, Enabled: true, TelegramIDs: []int64{o.Job.ChatID}}, o.Job.ChatID, 0, "refactor cron", false); err != nil {
		t.Fatal(err)
	}

	// Feed the watcher a change so the digest and brief have material.
	body := []byte("v1")
	fetch := func(context.Context, string) ([]byte, error) { return body, nil }
	wo := o
	wo.ScheduleID = "w1"
	wo.JobKind = "watcher"
	wo.Job.Text = "watch the price"
	if err := s.PutOccurrence(wo, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(wo.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(wo.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	body = []byte("v2")
	wo.DueAt = wo.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(wo, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(wo.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(wo.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	// The watcher's own change notification reaches the outbox.
	if d, err := s.ClaimDelivery(); err != nil || d == nil || !strings.Contains(d.Text, "v2") {
		t.Fatalf("watcher delivery: %+v %v", d, err)
	}

	// Digest: changed source lines plus the digest header.
	do := o
	do.ScheduleID = "d1"
	do.JobKind = "digest"
	do.Job.Text = "morning digest"
	do.DueAt = wo.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(do, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(do.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "watch the price") || !strings.Contains(d.Text, "v2") {
		t.Fatalf("digest delivery: %+v %v", d, err)
	}
	// Replay: the receipt dedupes a second dispatch of the same occurrence.
	if err := s.DispatchDueOccurrences(do.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.ClaimDelivery(); d != nil {
		t.Fatalf("replayed digest delivered twice: %+v", d)
	}

	// Brief: no new watcher changes after the digest consumed the generation,
	// but the open task still fills the snapshot — the brief rides the agent.
	bo := o
	bo.ScheduleID = "b1"
	bo.JobKind = "brief"
	bo.Job.Text = "morning brief"
	bo.DueAt = do.DueAt.Add(time.Hour)
	if err := s.PutOccurrence(bo, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(bo.DueAt, s.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || !strings.HasPrefix(job.ID, "routine-") || !strings.Contains(job.Text, "refactor cron") || !strings.Contains(job.Text, "untrusted snapshot") {
		t.Fatalf("brief job: %+v %v", job, err)
	}
	// The admitted bundle keeps the occurrence id so send-time
	// re-authorization resolves its schedule-pinned conversation.
	if err := s.authorizeOccurrence(*job); err != nil {
		t.Fatalf("brief bundle fails send-time re-authorization: %v", err)
	}

	// Event subscription fan-out — discrete events to the sink exactly
	// once, replay dedup through the durable seen-set, and auth-shaped
	// failures failing the occurrence — moved to the owner's real event
	// plane: internal/toolhub/automation/streams_ported_test.go
	// (TestSettleStreamEventsNotify, TestMCPEventEnvelopeFansOutByEventID,
	// TestSettleStreamEventsAuthDegraded).
}
