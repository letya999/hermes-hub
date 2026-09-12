package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func occurrenceFixture(t *testing.T) (*Spool, RoutineOccurrence, *Gateway) {
	t.Helper()
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "scheduled work"}
	o := RoutineOccurrence{ScheduleID: "schedule-one", Revision: 1, DueAt: time.Unix(100, 0), Job: job}
	g := &Gateway{spool: s, config: Config{OrganizationID: "personal"}, users: map[int64]User{11: {ID: "alice", Enabled: true}}, now: func() time.Time { return time.Unix(90, 0) }}
	return s, o, g
}
func TestDueOccurrenceSurvivesRestartAndEnqueuesExactlyOneOrdinaryJob(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt.Add(-time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("future occurrence executed")
	}
	restarted, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	s = restarted
	g.spool = s
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.Trigger != "cron" || job.ID != occurrenceID(o) || job.Envelope != o.Job.Envelope {
		t.Fatalf("ordinary job: %+v %v", job, err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("duplicate occurrence job")
	}
	// Simulate a crash after queue admission but before the occurrence settled.
	path := filepath.Join(s.root, "occurrences", occurrenceID(o)+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	_ = json.Unmarshal(b, &saved)
	saved.State = "pending"
	if err := atomicJSON(path, saved); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("handoff replay created duplicate")
	}
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.Job.Text = "different"
	if err := s.PutOccurrence(changed, changed.Job.Envelope); err == nil {
		t.Fatal("occurrence identity conflict accepted")
	}
}
func TestOccurrenceRejectsWrongOwnerSecretsRevocationAndExpiredCatchup(t *testing.T) {
	for _, kind := range []string{"owner", "secret", "revoked", "policy", "expired"} {
		t.Run(kind, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			caller := o.Job.Envelope
			switch kind {
			case "owner":
				caller.ExternalIdentityID = "other"
			case "secret":
				o.Job.Text = "GITHUB_TOKEN=secret"
			case "revoked":
				delete(g.users, 11)
			case "policy":
				t.Setenv("HUB_POLICY_VERSION", "policy-new")
			}
			err := s.PutOccurrence(o, caller)
			if kind == "owner" || kind == "secret" {
				if err == nil {
					t.Fatal("unsafe occurrence accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			now := o.DueAt
			if kind == "expired" {
				now = now.Add(2 * time.Hour)
			}
			if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
				t.Fatal(err)
			}
			if job, err := s.ClaimJob(); err != nil || job != nil {
				t.Fatal("unauthorized or expired occurrence executed")
			}
		})
	}
	s, o, _ := occurrenceFixture(t)
	if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, nil); err == nil {
		t.Fatal("missing authorizer accepted")
	}
	if err := s.DispatchDueOccurrences(o.DueAt, func(Job) error { return errors.New("revoked") }); err != nil {
		t.Fatal(err)
	}
}

func TestRunAtCommandPersistsVerifiedOccurrenceWithoutImmediateExecution(t *testing.T) {
	s, _, g := occurrenceFixture(t)
	update := Update{UpdateID: 77, Message: &Message{From: &TGUser{ID: 11}, Chat: TGChat{ID: 11, Type: "private"}, Text: "/runat 1970-01-01T00:01:40Z scheduled work"}}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("occurrences=%d err=%v", len(entries), err)
	}
	if job, err := s.ClaimJob(); err != nil || job != nil {
		t.Fatal("runat executed early")
	}
	if err := s.DispatchDueOccurrences(time.Unix(100, 0), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.Text != "scheduled work" || job.Trigger != "cron" {
		t.Fatalf("scheduled job: %+v %v", job, err)
	}
}

func TestOccurrencesDoNotOverlapAndUnknownRunContinuesToBlockSchedule(t *testing.T) {
	s, first, g := occurrenceFixture(t)
	second := first
	second.DueAt = first.DueAt.Add(time.Second)
	for _, o := range []RoutineOccurrence{second, first} {
		if err := s.PutOccurrence(o, o.Job.Envelope); err != nil {
			t.Fatal(err)
		}
	}
	now := second.DueAt
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimJob()
	if err != nil || job == nil || job.ID != occurrenceID(first) {
		t.Fatalf("earliest occurrence not selected: %+v %v", job, err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("overlapping occurrence admitted")
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "uncertain"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimJob(); err != nil || extra != nil {
		t.Fatal("unknown original run allowed overlap")
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{Status: "cancelled"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimJob()
	if err != nil || next == nil || next.ID != occurrenceID(second) {
		t.Fatalf("terminal run did not release schedule: %+v %v", next, err)
	}
}

func TestOccurrenceSerializedBoundRejectsEscapedOversize(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	o.Job.Text = strings.Repeat("\x01", 64*1024)
	if err := s.PutOccurrence(o, o.Job.Envelope); err == nil {
		t.Fatal("oversized durable occurrence accepted")
	}
}
