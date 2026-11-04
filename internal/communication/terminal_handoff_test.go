package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

type terminalPersistenceRunner struct{ beforeReturn func(Job) }

func (r terminalPersistenceRunner) Run(_ context.Context, job Job, _ User) (string, error) {
	r.beforeReturn(job)
	return "private fixture result", nil
}

type titleFailureRunner struct{ terminalPersistenceRunner }

func (titleFailureRunner) SessionUsage(context.Context, hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error) {
	return hubruntime.SessionUsage{}, errors.New("title fixture unavailable")
}

func TestTitleFailureKeepsCompletedResult(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := Job{Envelope: user.Envelope(11), ID: "title-failure", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "title-failure", ChatID: 11, TaskID: task.TaskID, Text: "fixture"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g.runner = titleFailureRunner{terminalPersistenceRunner{beforeReturn: func(Job) { cancel() }}}
	g.worker(ctx)
	if _, err := os.Stat(filepath.Join(g.spool.root, "done", spoolFileID(job.ID)+".json")); err != nil {
		t.Fatalf("completed job lost after title failure: %v", err)
	}
	if mapping, found, err := g.spool.Mapping(job.ID); err != nil || !found || mapping.Status != "completed" || mapping.Result != "private fixture result" {
		t.Fatalf("completed response lost after title failure: %+v %v", mapping, err)
	}
	if current, err := g.spool.TaskByID(user, 11, task.TaskID); err != nil || !current.NameAuto {
		t.Fatalf("failed title changed task: %+v %v", current, err)
	}
}

type terminalRecorder interface {
	RecordTerminalOutcome(Job, RunOutcome, *Delivery) error
}

func TestFailedOutcomeSaveDoesNotReportSuccess(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api = &fakeAPI{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	job := Job{Envelope: g.user("alice").Envelope(11), ID: "failed-save", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "failed-save", ChatID: 11, Text: "fixture"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g.runner = terminalPersistenceRunner{beforeReturn: func(job Job) {
		path := g.spool.mappingPath(job.ID)
		if err := os.Rename(path, path+".saved"); err != nil {
			t.Error(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Error(err)
		}
		cancel()
	}}
	g.worker(ctx)
	if _, err := os.Stat(filepath.Join(g.spool.root, "done", spoolFileID(job.ID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed outcome persistence acknowledged job completion")
	}
	for _, dir := range []string{"pending", "sending", "done"} {
		if _, err := os.Stat(filepath.Join(g.spool.root, "outbox", dir, spoolFileID("job-"+job.ID+"-response")+".json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed outcome persistence published a success delivery")
		}
	}
}

func TestTerminalReceiptCrashAtEachHandoffStep(t *testing.T) {
	for _, step := range []string{"receipt", "mapping", "outbox", "job"} {
		t.Run(step, func(t *testing.T) {
			s, job := streamJob(t)
			claimed, err := s.ClaimJob()
			if err != nil || claimed == nil {
				t.Fatal("fixture claim failed", err)
			}
			job = *claimed
			recorder, ok := any(s).(terminalRecorder)
			if !ok {
				t.Fatal("worker terminal path has no write-ahead receipt handoff")
			}
			outcome := RunOutcome{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", Text: "saved fixture answer", Voice: "spoken fixture"}
			delivery := Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, Text: outcome.Text, Voice: outcome.Voice}
			var restore func()
			switch step {
			case "receipt", "outbox":
				dir := filepath.Join(s.root, "run-events")
				if step == "outbox" {
					dir = filepath.Join(s.root, "outbox", "pending")
				}
				if err := os.Rename(dir, dir+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, []byte("fixture fault"), 0600); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(dir); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(dir+".saved", dir); err != nil {
						t.Fatal(err)
					}
				}
			case "mapping":
				path := s.conversationPath(job.PrincipalID, job.ContextID, job.ConversationID)
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			case "job":
				path := filepath.Join(s.root, "done", spoolFileID(job.ID)+".json")
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := recorder.RecordTerminalOutcome(job, outcome, &delivery); err == nil {
				t.Fatal("handoff fault reported success")
			}
			if step == "receipt" {
				m, _, err := s.Mapping(job.ID)
				if err != nil || m.Status == "completed" {
					t.Fatal("failed receipt advanced mapping")
				}
				restore()
				return
			}
			receipts, err := os.ReadDir(filepath.Join(s.root, "run-events"))
			if err != nil || len(receipts) != 1 {
				t.Fatal("terminal source receipt was not retained")
			}
			restore()
			recovered, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			m, found, err := recovered.Mapping(job.ID)
			if err != nil || !found || m.Status != "completed" || m.Result != outcome.Text {
				t.Fatal("terminal outcome lost on recovery")
			}
			if _, err := os.Stat(filepath.Join(s.root, "done", spoolFileID(job.ID)+".json")); err != nil {
				t.Fatal("recovery did not finalize the source job")
			}
			if next, err := recovered.ClaimJob(); err != nil || next != nil {
				t.Fatal("recovery resubmitted a completed prompt")
			}
			d, err := recovered.ClaimDelivery()
			if err != nil || d == nil || d.ID != delivery.ID || d.Text != outcome.Text || d.Voice != outcome.Voice {
				t.Fatal("exact delivery intent was not recovered")
			}
		})
	}
}

func TestRepeatedTerminalReceiptKeepsSingleDelivery(t *testing.T) {
	s, job := streamJob(t)
	recorder, ok := any(s).(terminalRecorder)
	if !ok {
		t.Fatal("worker terminal path has no write-ahead receipt handoff")
	}
	outcome := RunOutcome{JobID: job.ID, Status: "completed", Text: "fixture result", LastEvent: "run.completed"}
	d := Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, ChatID: job.ChatID, Channel: job.Channel, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: outcome.Text}
	if err := recorder.RecordTerminalOutcome(job, outcome, &d); err != nil {
		t.Fatal(err)
	}
	if err := recorder.RecordTerminalOutcome(job, outcome, &d); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(s.root, "run-events"))
	if err != nil || len(receipts) != 1 {
		t.Fatal("terminal outcome had multiple canonical receipts")
	}
	if first, err := s.ClaimDelivery(); err != nil || first == nil {
		t.Fatal("missing delivery")
	}
	if second, err := s.ClaimDelivery(); err != nil || second != nil {
		t.Fatal("terminal receipt duplicated delivery")
	}
}

func TestTerminalReceiptRejectsChangedDeliveryIntent(t *testing.T) {
	s, job := streamJob(t)
	outcome := RunOutcome{JobID: job.ID, Status: "completed", Text: "fixture"}
	d := Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, ChatID: job.ChatID, Channel: job.Channel, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: outcome.Text}
	if err := s.RecordTerminalOutcome(job, outcome, &d); err != nil {
		t.Fatal(err)
	}
	d.Text = "changed delivery"
	if err := s.RecordTerminalOutcome(job, outcome, &d); err == nil {
		t.Fatal("canonical delivery changed without a new operation")
	}
}

func TestTerminalReceiptRejectsStaleGeneration(t *testing.T) {
	s, job := streamJob(t)
	outcome := RunOutcome{JobID: job.ID, Status: "completed", RunID: "run-1", SessionID: "session-1", RuntimeGeneration: "gen-1", Text: "fixture"}
	if err := s.RecordTerminalOutcome(job, outcome, nil); err != nil {
		t.Fatal(err)
	}
	outcome.RuntimeGeneration = "gen-2"
	if err := s.RecordTerminalOutcome(job, outcome, nil); err == nil {
		t.Fatal("stale-generation terminal observation accepted")
	}
}

func TestTerminalReceiptWithoutDeliveryStillBindsRecoveryScope(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordTerminalOutcome(job, RunOutcome{JobID: job.ID, Status: "completed"}, nil); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.ContextID = "foreign"
	if err := s.writeMappingLocked(m); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("receipt without a delivery inherited foreign scope")
	}
}

func TestVoiceOnlyStreamHandoffKeepsSingleCanonicalReceipt(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.Voice = "completed", "run.completed", "voice-terminal", "spoken fixture"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	d := Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, ChatID: job.ChatID, Channel: job.Channel, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Voice: event.Voice}
	if err := s.RecordTerminalOutcome(job, outcomeFromEvent(event), &d); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(filepath.Join(s.root, "run-events"))
	if err != nil || len(receipts) != 1 {
		t.Fatal("stream and worker used different canonical receipts")
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Voice != event.Voice {
		t.Fatal("voice-only intent was dropped")
	}
}

func TestWorkerRequiresMatchingAdmissionBeforeRunner(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "foreign_context"}[mismatch], func(t *testing.T) {
			g, err := New(testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			g.api = &fakeAPI{}
			job := Job{Envelope: g.user("alice").Envelope(11), ID: "admission-preflight", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal", Channel: "telegram_bot", ChatID: 11, Text: "fixture"}
			if mismatch {
				job.IdempotencyKey = job.ID
			}
			if _, err := g.spool.Enqueue(job); err != nil {
				t.Fatal(err)
			}
			if mismatch {
				m, _, err := g.spool.Mapping(job.ID)
				if err != nil {
					t.Fatal(err)
				}
				m.ContextID = "foreign"
				if err := g.spool.writeMappingLocked(m); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			g.runner = terminalPersistenceRunner{beforeReturn: func(Job) { calls++ }}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			g.worker(ctx)
			if calls != 0 {
				t.Fatal("runner executed without matching durable admission")
			}
		})
	}
}

func TestClaimObservationKeepsOriginalPromptBinding(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Text != "" || claimed.TextSHA256 == "" || !m.MatchesJob(*claimed) {
		t.Fatal("observation lost prompt digest or kept executable input")
	}
}

func TestTerminalReceiptBeforeMappingKeepsOriginalGeneration(t *testing.T) {
	s, job := streamJob(t)
	m, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status = "completed"
	event.LastEvent = "run.completed"
	event.EventID = "handoff:completed"
	event.Text = "fixture"
	r := streamReceipt{JobID: job.ID, Position: 1, Binding: receiptBinding(m), FinalState: "done", Event: event}
	if err := writeRunReceipt(receiptPath(s.root, job.ID, event.EventID), r); err != nil {
		t.Fatal(err)
	}
	changed := outcomeFromEvent(event)
	changed.RuntimeGeneration = "foreign-generation"
	if err := s.RecordTerminalOutcome(job, changed, nil); err == nil {
		t.Fatal("pre-mapping receipt accepted a different generation")
	}
}

func TestStatusReceiptRecoveryKeepsLatestEditAndNeverResendsFailedEdit(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.LastEvent = "tool.start"
	event.EventID = "progress-one"
	event.Text = "first"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatal("status claim", err)
	}
	d.TelegramMessageID = 100
	if err := s.UpdateSending(*d); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	event.EventID = "progress-two"
	event.Text = "latest"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	edit, err := s.ClaimDelivery()
	if err != nil || edit == nil || edit.EditMessageID != 100 {
		t.Fatal("edit claim", err)
	}
	if err := s.UncertainDelivery(edit.ID); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.ClaimDelivery()
	if err != nil || again != nil {
		t.Fatal("recovery blindly resent a failed status edit", err)
	}
	event.EventID = "progress-three"
	event.Text = "newer"
	pending := filepath.Join(s.root, "outbox", "pending")
	if err := os.Rename(pending, pending+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pending, []byte("fixture fault"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := reopened.RecordStreamEvent(job, event); err == nil {
		t.Fatal("outbox fault was ignored")
	}
	if err := os.Remove(pending); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending+".saved", pending); err != nil {
		t.Fatal(err)
	}
	reopened, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := reopened.ClaimDelivery()
	if err != nil || latest == nil || latest.EditMessageID != 100 || latest.Text != "newer" {
		t.Fatal("recovery failed to retain latest editable status", err)
	}
}

func TestUncertainHandoffSurvivesTrustedGenerationRebind(t *testing.T) {
	s, job := streamJob(t)
	first := RunOutcome{JobID: job.ID, RunID: "run-1", SessionID: "session-1", RuntimeGeneration: "gen-1", Status: "uncertain", LastEvent: "run.unknown"}
	if err := s.RecordTerminalOutcome(job, first, nil); err != nil {
		t.Fatal(err)
	}
	rebound := streamEvent(job)
	rebound.RuntimeGeneration = "gen-2"
	rebound.EventID = "rebound-admission"
	if err := s.RebindObservation(job, rebound); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, rebound); err != nil {
		t.Fatal(err)
	}
	second := first
	second.RuntimeGeneration = "gen-2"
	if err := s.RecordTerminalOutcome(job, second, nil); err != nil {
		t.Fatal("trusted recovery could not save next uncertain observation", err)
	}
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := reopened.Mapping(job.ID)
	if err != nil || m.RuntimeGeneration != "gen-2" || m.Status != "uncertain" {
		t.Fatal("recovery rewound generation or outcome", err)
	}
	var current int
	records, err := os.ReadDir(filepath.Join(s.root, "run-events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		body, err := os.ReadFile(filepath.Join(s.root, "run-events", record.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var receipt streamReceipt
		if json.Unmarshal(body, &receipt) != nil {
			t.Fatal("invalid receipt")
		}
		if receipt.Event.Status == "uncertain" && receipt.Event.RuntimeGeneration == "gen-2" {
			current++
		}
	}
	if current != 1 {
		t.Fatal("new uncertain generation has no single canonical outcome")
	}
}

// Re-admission regresses the mapping to running while the finished uncertain
// receipt still owns the outcome; the next identical uncertain handoff must
// re-assert that receipt — reporting it stale strands the job in running and
// deadlocks the conversation lane (live incident: telegram-837845960).
func TestUncertainReclaimReassertsFinishedReceipt(t *testing.T) {
	s, job := streamJob(t)
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	outcome := RunOutcome{JobID: job.ID, Status: "uncertain", LastEvent: "run.unknown"}
	if err := s.RecordTerminalOutcome(*claimed, outcome, nil); err != nil {
		t.Fatal(err)
	}
	first, _, err := s.Mapping(job.ID)
	if err != nil || first.Status != "uncertain" || first.UncertainSince.IsZero() {
		t.Fatalf("first uncertain outcome not recorded: %+v %v", first, err)
	}
	if err := s.RequeueObservations(time.Now(), true); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := s.ClaimJob()
	if err != nil || reclaimed == nil || reclaimed.ID != job.ID {
		t.Fatalf("reclaim: %v %v", reclaimed, err)
	}
	queued := Job{Envelope: job.Envelope, ID: "stream-job-2", OrganizationID: job.OrganizationID, UserID: job.UserID, ActorID: job.ActorID, ScopeID: job.ScopeID, Channel: job.Channel, Trigger: job.Trigger, IdempotencyKey: "stream-key-2", ChatID: job.ChatID, Text: "next"}
	if _, err := s.Enqueue(queued); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordTerminalOutcome(*reclaimed, outcome, nil); err != nil {
		t.Fatalf("re-admitted uncertain outcome mislabeled stale: %v", err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil || m.Status != "uncertain" || !m.UncertainSince.Equal(first.UncertainSince) {
		t.Fatalf("outcome not re-asserted at first-uncertain anchor: %+v %v", m, err)
	}
	if _, err := os.Lstat(filepath.Join(s.root, "failed", spoolFileID(job.ID)+".json")); err != nil {
		t.Fatalf("job did not return to failed: %v", err)
	}
	next, err := s.ClaimJob()
	if err != nil || next == nil || next.ID != queued.ID {
		t.Fatalf("conversation lane still held: %v %v", next, err)
	}
}
