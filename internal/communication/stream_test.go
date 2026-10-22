package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func streamJob(t *testing.T) (*Spool, Job) {
	t.Helper()
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "stream-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "stream-key", ChatID: 11, Text: "hello"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	return s, job
}

func streamEvent(job Job) hubruntime.ExecuteResponse {
	return hubruntime.ExecuteResponse{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "running", LastEvent: "run.admitted", EventID: "admitted"}
}

// Live progress: while a telegram run is non-terminal the gateway re-arms the
// typing action and refreshes the single status message with elapsed time;
// terminal jobs are left alone.
func TestLiveProgressTickRearmsAndAges(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_800_000_000, 0)
	envelope := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	envelope.DeliveryTargetID = "telegram--100"
	job := Job{Envelope: envelope, ID: "live-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "live-key", ChatID: -100, TopicID: 5, Text: "hello", CreatedAt: base}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	g.now = func() time.Time { return base }
	ctx := context.Background()
	started := streamEvent(job)
	started.EventID, started.LastEvent = "started", "run.started"
	if err := s.RecordStreamEvent(job, started); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 {
		t.Fatalf("status send lost: %v", fake.sent)
	}
	g.now = func() time.Time { return base.Add(65 * time.Second) }
	g.progressTick(ctx)
	actions := len(fake.chatActions)
	if actions == 0 || fake.chatActions[actions-1].action != "typing" {
		t.Fatalf("progress tick did not re-arm typing: %+v", fake.chatActions)
	}
	g.deliverOne(ctx)
	if len(fake.edits) == 0 {
		t.Fatalf("no elapsed-time edit queued: sent=%v", fake.sent)
	}
	last := fake.edits[len(fake.edits)-1]
	if last.messageID != 100 || !strings.Contains(last.text, "Выполняю запрос") || !strings.Contains(last.text, "1 мин") {
		t.Fatalf("status edit lost base text or elapsed: %+v", last)
	}
	// Once the run is terminal the tick produces neither actions nor edits.
	terminal := streamEvent(job)
	terminal.Status, terminal.LastEvent, terminal.EventID, terminal.Text = "completed", "run.completed", "done", "готово"
	if err := s.RecordStreamEvent(job, terminal); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	actions, edits := len(fake.chatActions), len(fake.edits)
	g.progressTick(ctx)
	g.deliverOne(ctx)
	if len(fake.chatActions) != actions || len(fake.edits) != edits {
		t.Fatalf("terminal job still gets progress: actions=%d edits=%d", len(fake.chatActions), len(fake.edits))
	}
}

// A queued job that never reached admission — a dead supervisor, a wedged
// runtime — must not farm typing actions and status edits forever.
func TestProgressTickSkipsUnadmittedJob(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "stuck-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "stuck-key", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.spool = fake, s
	g.progressTick(context.Background())
	g.deliverOne(context.Background())
	if len(fake.chatActions) != 0 || len(fake.sent) != 0 {
		t.Fatalf("unadmitted job got progress: actions=%v sent=%v", fake.chatActions, fake.sent)
	}
}

// The gateway's run loop schedules the periodic progress tick: a non-terminal
// telegram job gets its typing action re-armed while Run is alive.
func TestProgressLoopRunsDuringGatewayRun(t *testing.T) {
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	job := Job{Envelope: user.Envelope(11), ID: "loop-live", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "loop-key", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	fake := &idleAPI{}
	g.api, g.runner = fake, blockingRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		fake.mu.Lock()
		n := len(fake.chatActions)
		fake.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("progress loop never re-armed typing during Run")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("run exit: %v", err)
	}
}

// idleAPI long-polls forever so Run's update loop respects cancellation.
type idleAPI struct{ fakeAPI }

func (a *idleAPI) GetUpdates(ctx context.Context, _ int64, _ int) ([]Update, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTerminalFailureDeliveryNamesCauseWithoutRawError(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			s, job := streamJob(t)
			event := streamEvent(job)
			event.Status, event.LastEvent, event.EventID = status, "run."+status, "terminal"
			event.Text = "provider secret must not reach chat"
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			delivery, err := s.ClaimDelivery()
			if err != nil || delivery == nil || !strings.Contains(delivery.Text, job.ID) || !strings.Contains(delivery.Text, "run."+status) || strings.Contains(delivery.Text, "provider secret") {
				t.Fatalf("failure must carry a safe named cause and correlation ID: %+v %v", delivery, err)
			}
		})
	}
}

func TestProgressDeliveryPreservesUsefulTextAndDropsEmptyToolNoise(t *testing.T) {
	s, job := streamJob(t)
	progress := streamEvent(job)
	progress.EventID, progress.LastEvent, progress.Text = "tool:prepare", "tool.start", "MCP: шаг 1/4 — собираю образ."
	if err := s.RecordStreamEvent(job, progress); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != progress.Text {
		t.Fatalf("useful progress lost: %+v %v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	repeated := progress
	repeated.EventID = "tool:prepare-again"
	if err := s.RecordStreamEvent(job, repeated); err != nil {
		t.Fatal(err)
	}
	if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
		t.Fatalf("repeated progress produced spam: %+v %v", delivery, err)
	}
	noise := progress
	noise.EventID, noise.Text = "tool:noise", ""
	if err := s.RecordStreamEvent(job, noise); err != nil {
		t.Fatal(err)
	}
	if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
		t.Fatalf("empty tool progress produced spam: %+v %v", delivery, err)
	}
}

// SPEC-0053 R2.2: progress lands in one editable status message per job —
// later events edit the same Telegram message instead of flooding the chat.
func TestProgressUsesSingleEditedMessage(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	started := streamEvent(job)
	started.EventID, started.LastEvent, started.Status, started.Text = "started", "run.started", "running", ""
	if err := s.RecordStreamEvent(job, started); err != nil {
		t.Fatal(err)
	}
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 || len(fake.edits) != 0 {
		t.Fatalf("first progress must send one message, got sent=%v edits=%v", fake.sent, fake.edits)
	}
	step2 := streamEvent(job)
	step2.EventID, step2.LastEvent, step2.Text = "tool:2", "tool.start", "MCP: шаг 2/4."
	if err := s.RecordStreamEvent(job, step2); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("second progress sent a new message: %v", fake.sent)
	}
	if len(fake.edits) != 1 || fake.edits[0].messageID != 100 || fake.edits[0].text != "MCP: шаг 2/4." {
		t.Fatalf("second progress must edit message 100: %v", fake.edits)
	}
}

// SPEC-0053 R2.2: a gateway restart must reuse the persisted status receipt —
// replayed events stay idempotent and the next update edits the same message.
func TestStatusSurvivesGatewayRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "stream-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "stream-key", ChatID: 11, Text: "hello"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("status send lost: %v", fake.sent)
	}
	reopened, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g2.api, g2.spool = fake, reopened
	step2 := streamEvent(job)
	step2.EventID, step2.LastEvent, step2.Text = "tool:2", "tool.start", "MCP: шаг 2/4."
	if err := reopened.RecordStreamEvent(job, step2); err != nil {
		t.Fatal(err)
	}
	g2.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("restart produced a duplicate message: %v", fake.sent)
	}
	if len(fake.edits) != 1 || fake.edits[0].messageID != 100 || fake.edits[0].text != "MCP: шаг 2/4." {
		t.Fatalf("post-restart progress must edit message 100: %v", fake.edits)
	}
}

// SPEC-0053 R2.2: status edits are rate-limited to one per two seconds per
// chat, and a burst of updates collapses to the newest text instead of a
// staircase of stale edits.
func TestProgressEditRateLimited(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	now := time.Unix(1_700_000_000, 0)
	g.now = func() time.Time { return now }
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	for i, text := range []string{"MCP: шаг 2/4.", "MCP: шаг 3/4."} {
		ev := streamEvent(job)
		ev.EventID, ev.LastEvent, ev.Text = fmt.Sprintf("tool:%d", i+2), "tool.start", text
		if err := s.RecordStreamEvent(job, ev); err != nil {
			t.Fatal(err)
		}
	}
	g.deliverOne(context.Background())
	if len(fake.edits) != 1 || fake.edits[0].text != "MCP: шаг 3/4." {
		t.Fatalf("burst must collapse to one newest edit: %v", fake.edits)
	}
	ev := streamEvent(job)
	ev.EventID, ev.LastEvent, ev.Text = "tool:4", "tool.start", "MCP: шаг 4/4."
	if err := s.RecordStreamEvent(job, ev); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.edits) != 1 {
		t.Fatalf("edit inside the 2s window must be deferred: %v", fake.edits)
	}
	now = now.Add(2 * time.Second)
	g.deliverOne(context.Background())
	if len(fake.edits) != 2 || fake.edits[1].text != "MCP: шаг 4/4." {
		t.Fatalf("deferred edit must land after the window: %v", fake.edits)
	}
}

// SPEC-0053 R2.2: the final response retires the status message — the chat
// ends with one answer and no stale progress line, and late events cannot
// resurrect it.
func TestStatusMessageFinalizedOnResponse(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("status send lost: %v", fake.sent)
	}
	done := streamEvent(job)
	done.EventID, done.Status, done.LastEvent, done.Text = "done", "completed", "run.completed", "Готово."
	if err := s.RecordStreamEvent(job, done); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.deleted) != 1 || fake.deleted[0] != 100 {
		t.Fatalf("status message must be deleted on final response: %v", fake.deleted)
	}
	late := streamEvent(job)
	late.EventID, late.LastEvent, late.Text = "tool:late", "tool.start", "MCP: поздний шаг."
	// A late non-terminal event may be rejected by stream validation or,
	// if it does reach the spool, must die on the closed status receipt.
	_ = s.RecordStreamEvent(job, late)
	g.deliverOne(context.Background())
	if len(fake.sent) != 2 || len(fake.edits) != 0 {
		t.Fatalf("late progress resurrected the status: sent=%v edits=%v", fake.sent, fake.edits)
	}
}

// SPEC-0053 R2.2: a failed status edit must not bounce into a new send — the
// failed record parks and the next event edits the same message again, so the
// chat never gains a duplicate.
func TestEditFailureFallsBackWithoutDuplicates(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100}
	g.api, g.spool = fake, s
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	fake.err = errors.New("edit failed")
	step2 := streamEvent(job)
	step2.EventID, step2.LastEvent, step2.Text = "tool:2", "tool.start", "MCP: шаг 2/4."
	if err := s.RecordStreamEvent(job, step2); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	fake.err = nil
	if len(fake.sent) != 1 {
		t.Fatalf("failed edit produced a duplicate message: %v", fake.sent)
	}
	step3 := streamEvent(job)
	step3.EventID, step3.LastEvent, step3.Text = "tool:3", "tool.start", "MCP: шаг 3/4."
	if err := s.RecordStreamEvent(job, step3); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 1 {
		t.Fatalf("recovery sent a new message: %v", fake.sent)
	}
	if len(fake.edits) != 2 || fake.edits[1].messageID != 100 || fake.edits[1].text != "MCP: шаг 3/4." {
		t.Fatalf("recovery must edit message 100 again: %v", fake.edits)
	}
}

// SPEC-0053 R2.2: when the status send itself was uncertain, the spool must
// not blind-resend a fresh status message — a possibly-delivered message can
// never be duplicated, only abandoned.
func TestUncertainStatusSendIsNotResent(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{messageSeq: 100, err: errors.New("send timeout")}
	g.api, g.spool = fake, s
	step := streamEvent(job)
	step.EventID, step.LastEvent, step.Text = "tool:1", "tool.start", "MCP: шаг 1/4."
	if err := s.RecordStreamEvent(job, step); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	fake.err = nil
	step2 := streamEvent(job)
	step2.EventID, step2.LastEvent, step2.Text = "tool:2", "tool.start", "MCP: шаг 2/4."
	if err := s.RecordStreamEvent(job, step2); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) != 0 {
		t.Fatalf("uncertain status send was retried: %v", fake.sent)
	}
	if len(fake.edits) != 0 {
		t.Fatalf("no message ID was persisted, nothing may be edited: %v", fake.edits)
	}
}

// SPEC-0053 R2.3: enqueue and completion wake the loops through a notify
// channel instead of relying on the fallback poll.
func TestEnqueueWakesWorkerWithoutPoll(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	drain := func() {
		for {
			select {
			case <-s.Notify():
			default:
				return
			}
		}
	}
	expectWake := func(what string) {
		t.Helper()
		select {
		case <-s.Notify():
		case <-time.After(time.Second):
			t.Fatalf("%s did not signal the spool", what)
		}
	}
	drain()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "wake-job", OrganizationID: "personal", UserID: "alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "wake-key", ChatID: 11, Text: "hello"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	expectWake("job enqueue")
	drain()
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatalf("job was not claimable: %v %v", claimed, err)
	}
	if err := s.CompleteJob(claimed.ID); err != nil {
		t.Fatal(err)
	}
	expectWake("job completion")
	drain()
	if err := s.EnqueueDelivery(Delivery{ID: "wake-delivery", Channel: "telegram_bot", ChatID: 11, Text: "x", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	expectWake("delivery enqueue")
	drain()
	if err := s.UpsertStatusDelivery(Delivery{ID: "job-wake-job-status", Channel: "telegram_bot", ChatID: 11, Text: "progress", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	expectWake("status upsert")
}

// SPEC-0053 R2.3: the worker loop selects on the notify channel — a job
// enqueued while the loop idles is claimed promptly, not on the next
// one-second fallback tick.
func TestWorkerDrainsEnqueuedJobOnNotify(t *testing.T) {
	s, job := streamJob(t)
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api, g.spool, g.runner = &fakeAPI{}, s, &fakeRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); g.worker(ctx) }()
	defer func() { cancel(); wg.Wait() }()
	waitDone := func(id string, bound time.Duration) {
		t.Helper()
		deadline := time.Now().Add(bound)
		for {
			if m, found, err := s.Mapping(id); err == nil && found && m.Status == "completed" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s not completed within %v", id, bound)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitDone(job.ID, 2*time.Second)
	time.Sleep(300 * time.Millisecond) // the loop is parked in select now
	second := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "wake-job-2", OrganizationID: "personal", UserID: "alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "wake-key-2", ChatID: 11, Text: "again"}
	if _, err := s.Enqueue(second); err != nil {
		t.Fatal(err)
	}
	waitDone(second.ID, 500*time.Millisecond)
}

// SPEC-0053 R2.3: with nothing to do the worker sleeps — a ~350ms idle window
// must not show a staircase of pending-job scans.
func TestWorkerIdlesBetweenSignals(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	g.api, g.spool, g.runner = &fakeAPI{}, s, &fakeRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.worker(ctx)
	time.Sleep(150 * time.Millisecond) // startup pass settles
	before := s.ClaimPolls()
	time.Sleep(350 * time.Millisecond)
	if polls := s.ClaimPolls() - before; polls > 1 {
		t.Fatalf("idle worker polled %d times in 350ms; want at most one fallback tick", polls)
	}
}

// SPEC-0053 R2.3: the outbox loop is event-driven — an enqueued delivery
// leaves promptly on the notify signal while an idle loop barely scans.
func TestDeliveryLoopDrainsOnNotify(t *testing.T) {
	s, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(testConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.spool = fake, s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.deliveryLoop(ctx)
	time.Sleep(150 * time.Millisecond) // startup pass settles
	before := s.DeliverPolls()
	time.Sleep(350 * time.Millisecond)
	if polls := s.DeliverPolls() - before; polls > 1 {
		t.Fatalf("idle delivery loop polled %d times in 350ms; want at most one fallback tick", polls)
	}
	if err := s.EnqueueDelivery(Delivery{ID: "notify-delivery", Channel: "telegram_bot", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		fake.mu.Lock()
		sent := len(fake.sent)
		fake.mu.Unlock()
		if sent > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("enqueued delivery not sent promptly")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// SPEC-0053 R2.3: a control request wakes the control loop instead of waiting
// for the fallback tick, and an idle loop stops scanning every mapping.
func TestControlLoopWakesOnCancelRequest(t *testing.T) {
	var controlCalls atomic.Int64
	rt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"job_id": "stream-job", "status": "cancelled"})
	}))
	defer rt.Close()
	s, job := streamJob(t)
	config := testConfig(t)
	config.RuntimeURL, config.RuntimeAuth = rt.URL, "token"
	g, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	g.api, g.spool = &fakeAPI{}, s
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.controlLoop(ctx)
	}()
	defer func() { cancel(); <-done }()
	time.Sleep(150 * time.Millisecond)
	before := g.controlPolls.Load()
	time.Sleep(350 * time.Millisecond)
	if polls := g.controlPolls.Load() - before; polls > 1 {
		t.Fatalf("idle control loop scanned %d times in 350ms; want at most one fallback tick", polls)
	}
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestCancel(job.ID, job.Envelope); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	for controlCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("cancel request did not wake the control loop")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// SPEC-0053 R2.3: mappings written before the by-key index existed get
// indexed at startup; lookups then resolve through the index, not a scan.
func TestIndexBuiltForLegacyMappings(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "legacy-job", OrganizationID: "personal", UserID: "alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "legacy-key", ChatID: 11, Text: "hi"}
	if _, err := s.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-index spool: drop the index entry and reopen.
	entries, err := os.ReadDir(filepath.Join(root, "mappings", "by-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(root, "mappings", "by-key", entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.recover(); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("legacy-key"))
	indexPath := filepath.Join(root, "mappings", "by-key", hex.EncodeToString(key[:])+".json")
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("startup did not rebuild the idempotency index: %v", err)
	}
	if _, found, err := reopened.findIdempotencyLocked("legacy-key"); err != nil || !found {
		t.Fatalf("indexed lookup failed: found=%v err=%v", found, err)
	}
}

// SPEC-0053 R2.3: idempotency resolution must stay constant-time as the
// mapping set grows — the by-key index replaces the linear scan.
func BenchmarkIdempotencyLookup10k(b *testing.B) {
	root := filepath.Join(b.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		b.Fatal(err)
	}
	target := ""
	for i := 0; i < 10_000; i++ {
		key := fmt.Sprintf("key-%05d", i)
		mapping := JobMapping{JobID: fmt.Sprintf("job-%05d", i), IdempotencyKey: key, Status: "completed"}
		s.mu.Lock()
		if err := s.writeMappingLocked(mapping); err != nil {
			s.mu.Unlock()
			b.Fatal(err)
		}
		s.mu.Unlock()
		target = key
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.mu.Lock()
		mapping, found, err := s.findIdempotencyLocked(target)
		s.mu.Unlock()
		if err != nil || !found || mapping.JobID != "job-09999" {
			b.Fatalf("lookup failed: found=%v err=%v", found, err)
		}
	}
}

// SPEC-0053 R2.3: the notify channel makes enqueue->claim latency
// sub-millisecond instead of bounded by the idle poll cadence.
func BenchmarkEnqueueToClaimLatency(b *testing.B) {
	root := filepath.Join(b.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job := Job{ID: fmt.Sprintf("job-%d", i), ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", ChatID: 11, IdempotencyKey: fmt.Sprintf("key-%d", i), Text: "hello"}
		if _, err := s.Enqueue(job); err != nil {
			b.Fatal(err)
		}
		select {
		case <-s.Notify():
		case <-time.After(time.Second):
			b.Fatal("enqueue did not signal")
		}
		claimed, err := s.ClaimJob()
		if err != nil || claimed == nil {
			b.Fatalf("claim failed: %v", err)
		}
		_ = s.CompleteJob(claimed.ID)
	}
}

// SPEC-0053 R2.3: retention drops aged terminal records but never touches
// live work — non-terminal mappings, pending jobs, and open approvals stay.
func TestCompactionKeepsNonTerminal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * time.Hour)
	s.mu.Lock()
	for _, m := range []JobMapping{
		{JobID: "old-done", IdempotencyKey: "k-old", Status: "completed"},
		{JobID: "old-running", IdempotencyKey: "k-run", Status: "running"},
		{JobID: "old-approval", IdempotencyKey: "k-appr", Status: "failed", ApprovalState: "pending"},
	} {
		if err := s.writeMappingLocked(m); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	s.mu.Unlock()
	for _, id := range []string{"old-done", "old-running", "old-approval"} {
		_ = os.Chtimes(s.mappingPath(id), old, old)
	}
	if err := atomicJSON(filepath.Join(root, "done", "old-job.json"), Job{ID: "old-job"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(root, "done", "old-job.json"), old, old)
	if err := atomicJSON(filepath.Join(root, "pending", "new-job.json"), Job{ID: "new-job"}); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(root, "pending", "new-job.json"), old, old)
	if err := s.Compact(time.Now(), 336*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.mappingPath("old-done")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aged terminal mapping survived compaction")
	}
	if _, err := os.Stat(s.idempotencyIndexPath("k-old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale idempotency index entry survived compaction")
	}
	for _, id := range []string{"old-running", "old-approval"} {
		if _, err := os.Stat(s.mappingPath(id)); err != nil {
			t.Fatalf("live mapping %s compacted: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "done", "old-job.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aged done record survived compaction")
	}
	if _, err := os.Stat(filepath.Join(root, "pending", "new-job.json")); err != nil {
		t.Fatal("pending job compacted")
	}
}

// SPEC-0053 R2.3: an events receipt whose delivery is still in flight is live
// state, not history — compaction must keep it.
func TestCompactionKeepsPendingReceipts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-400 * time.Hour)
	receipt := func(name, deliveryID string) string {
		t.Helper()
		path := filepath.Join(root, "events", name+".json")
		if err := atomicJSON(path, streamReceipt{JobID: "job-x", Position: 1, Event: hubruntime.ExecuteResponse{JobID: "job-x", EventID: name, Status: "running", LastEvent: "run.started"}, Delivery: &Delivery{ID: deliveryID}}); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, old, old)
		return path
	}
	kept := receipt("live", "job-x-event-live")
	dropped := receipt("stale", "job-x-event-stale")
	if err := atomicJSON(filepath.Join(root, "outbox", "pending", "job-x-event-live.json"), Delivery{ID: "job-x-event-live", Channel: "telegram_bot", ChatID: 11, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now(), 336*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal("receipt backing a pending delivery was compacted")
	}
	if _, err := os.Stat(dropped); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreferenced aged receipt survived compaction")
	}
}

// SPEC-0053 R2.3: retention runs on the control loop's cadence — aged
// terminal records disappear without an operator stepping in.
func TestControlLoopCompactsAgedTerminal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "spool")
	s, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	if err := s.writeMappingLocked(JobMapping{JobID: "aged-job", IdempotencyKey: "k-aged", Status: "completed"}); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	old := time.Now().Add(-400 * time.Hour)
	_ = os.Chtimes(s.mappingPath("aged-job"), old, old)
	config := testConfig(t)
	config.SpoolRetention = time.Hour
	g, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	g.api, g.spool = &fakeAPI{}, s
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.controlLoop(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(s.mappingPath("aged-job")); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("control loop did not compact the aged terminal mapping")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Keepalive frames are transport-only: they must survive validation yet never
// touch the durable mapping or the outbox.
func TestHeartbeatNotPersistedOrDelivered(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	heartbeat := hubruntime.ExecuteResponse{JobID: job.ID, Status: "running", LastEvent: "run.heartbeat"}
	if err := s.RecordStreamEvent(job, heartbeat); err != nil {
		t.Fatalf("keepalive rejected: %v", err)
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	if mapping.EventPosition != 1 || mapping.LastEventID != "admitted" || mapping.Status != "running" {
		t.Fatalf("heartbeat mutated durable state: %+v", mapping)
	}
	if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
		t.Fatalf("keepalive produced a delivery: %+v %v", delivery, err)
	}
}

func TestCachedTerminalResponseRepairsDeliveryWithoutWorker(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	final := streamEvent(job)
	final.Status, final.Text, final.RuntimeGeneration = "completed", "cached answer", "gen-2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/resume":
			_ = json.NewEncoder(w).Encode(final)
		case "/v1/usage":
			http.NotFound(w, r)
		default:
			t.Errorf("replayed original request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, Auth: "secret", Spool: s, HTTP: server.Client()}
	if _, err := runner.RunOutcome(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("no durable cached delivery: %+v %v", delivery, err)
	}
	// Crash after the receipt, before the worker can hand off to Telegram.
	if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	delivery, err = s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "cached answer" {
		t.Fatalf("cached final lost: %+v %v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, finalWithID(final)); err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDelivery(); err != nil || next != nil {
		t.Fatalf("duplicate cached final: %+v %v", next, err)
	}
}

func TestRestartRejectsCorruptAdmissionInsteadOfReplayingPrompt(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.mappingPath(job.ID), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("corrupt admission metadata accepted")
	}
	if _, err := os.Stat(filepath.Join(s.root, "running", job.ID+".json")); err != nil {
		t.Fatalf("claimed job moved into replay queue: %v", err)
	}
}

func TestTerminalErrorReceiptSurvivesCrashWithoutLeakingDetails(t *testing.T) {
	for _, status := range []string{"failed", "cancelled", "interrupted"} {
		t.Run(status, func(t *testing.T) {
			s, job := streamJob(t)
			event := finalWithID(streamEvent(job))
			event.Status, event.EventID, event.LastEvent, event.Text = status, "terminal:"+status, "run."+status, "PRIVATE_ERROR_CANARY"
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			delivery, err := s.ClaimDelivery()
			if err != nil || delivery == nil || strings.Contains(delivery.Text, "PRIVATE_ERROR_CANARY") || delivery.JobID != "" {
				t.Fatalf("missing/private/restart-triggering error delivery: %+v %v", delivery, err)
			}
			if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
				t.Fatal(err)
			}
			s, err = NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			delivery, err = s.ClaimDelivery()
			if err != nil || delivery == nil || delivery.ID != "job-"+job.ID+"-error" {
				t.Fatalf("terminal error handoff lost: %+v %v", delivery, err)
			}
			if err := s.CompleteDelivery(delivery.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordStreamEvent(job, event); err != nil {
				t.Fatal(err)
			}
			if duplicate, err := s.ClaimDelivery(); err != nil || duplicate != nil {
				t.Fatalf("duplicate terminal error: %+v %v", duplicate, err)
			}
		})
	}
}

func finalWithID(event hubruntime.ExecuteResponse) hubruntime.ExecuteResponse {
	event.EventID, event.LastEvent = "terminal:"+event.Status, "run."+event.Status
	return event
}

func TestTelegramOutputLimitsPreserveLongUnicodeResult(t *testing.T) {
	text := strings.Repeat("я", 4097)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/bottoken/sendMessage":
			var payload struct {
				Text      string `json:"text"`
				ParseMode string `json:"parse_mode"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Text != text || payload.ParseMode != "HTML" {
				t.Error("invalid bounded message")
			}
		case "/bottoken/sendDocument":
			if r.ParseMultipartForm(3<<20) != nil || r.FormValue("chat_id") != "11" {
				t.Error("invalid document")
				return
			}
			file, header, err := r.FormFile("document")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			contents, err := io.ReadAll(file)
			if err != nil || string(contents) != text || header.Filename != "answer.md" {
				t.Error("document truncated or changed")
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()
	api := newTelegramAPI(server.URL, "token", 0)
	if err := api.SendMessage(context.Background(), 11, 0, text, "HTML"); err != nil {
		t.Fatal(err)
	}
	if err := api.SendDocument(context.Background(), 11, 0, "answer.md", "", []byte(text)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", string([]byte{255}), strings.Repeat("x", 2*1024*1024+1)} {
		if err := api.SendMessage(context.Background(), 11, 0, invalid, ""); err == nil {
			t.Error("invalid output dispatched")
		}
	}
	if err := api.SendDocument(context.Background(), 11, 0, "answer.md", "", nil); err == nil {
		t.Error("empty document dispatched")
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestStreamReceiptsDeduplicateAndRepairOutboxOnRestart(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal:completed", "answer"
	for range 2 {
		if err := s.RecordStreamEvent(job, event); err != nil {
			t.Fatal(err)
		}
	}
	changed := event
	changed.EventID, changed.Text = "another-terminal", "different answer"
	if err := s.RecordStreamEvent(job, changed); err == nil {
		t.Fatal("late terminal replaced authoritative result")
	}
	if err := s.RecordOutcome(job.ID, outcomeFromEvent(changed)); err == nil {
		t.Fatal("JSON terminal replaced authoritative result")
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 2 {
		t.Fatalf("position=%d", m.EventPosition)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.Text != "answer" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
	// Simulate a crash between the durable receipt and outbox creation.
	if err := os.Remove(filepath.Join(s.root, "outbox", "sending", delivery.ID+".json")); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ = s.Mapping(job.ID)
	if m.Status != "completed" {
		t.Fatalf("terminal state lost: %+v", m)
	}
	delivery, err = s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.ID != "job-"+job.ID+"-response" {
		t.Fatalf("recovered delivery=%+v err=%v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if next, err := s.ClaimDelivery(); err != nil || next != nil {
		t.Fatalf("duplicate final=%+v err=%v", next, err)
	}
}

func TestStreamRejectsChangedBindingAndBoundsBeforeReceipt(t *testing.T) {
	s, job := streamJob(t)
	initial := streamEvent(job)
	if err := s.RecordStreamEvent(job, initial); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generation", "run", "session", "event", "output", "conversation", "external", "policy", "runtime", "schema", "terminal", "actor", "user", "organization", "scope", "channel", "trigger", "idempotency"} {
		event := initial
		event.EventID = "next"
		altered := job
		switch field {
		case "generation":
			event.RuntimeGeneration = "gen-2"
		case "run":
			event.RunID = "run-2"
		case "session":
			event.SessionID = "session-2"
		case "event":
			event.EventID = ""
		case "output":
			event.Text = strings.Repeat("x", 2*1024*1024+1)
		case "conversation":
			altered.ConversationID = "another"
		case "external":
			altered.ExternalIdentityID = "another"
		case "policy":
			altered.PolicyVersion = "another"
		case "runtime":
			altered.RuntimeID = "another"
		case "schema":
			altered.Schema = 0
		case "terminal":
			event.Status, event.LastEvent, event.Text = "completed", "tool.start", "private tool arguments"
		case "actor":
			altered.ActorID = "another"
		case "user":
			altered.UserID = "another"
		case "organization":
			altered.OrganizationID = "another"
		case "scope":
			altered.ScopeID = "another"
		case "channel":
			altered.Channel = "another"
		case "trigger":
			altered.Trigger = "another"
		case "idempotency":
			altered.IdempotencyKey = "another"
		}
		if err := s.RecordStreamEvent(altered, event); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 1 {
		t.Fatalf("rejected event wrote state: %+v", m)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", "broken.json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

// A replayed uncertain record is still uncertain — the caller keeps
// re-observing instead of treating the durable replay as malformed.
func TestReplayedUncertainStaysUncertain(t *testing.T) {
	s, job := streamJob(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: job.ID, SessionID: "session-1", RunID: "run-1", RuntimeGeneration: "gen-1", Status: "uncertain", LastEvent: "run.unknown"})
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
	if !errors.Is(err, ErrUncertain) || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

// A heartbeat frame carries no run identity. When the stream dies after
// admission, the uncertain outcome must still hold the bound run so recovery
// re-observes the original run instead of treating the job as never admitted.
func TestHeartbeatDoesNotClobberBoundRunIdentity(t *testing.T) {
	s, job := streamJob(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		_ = json.NewEncoder(w).Encode(streamEvent(job))
		_ = json.NewEncoder(w).Encode(hubruntime.ExecuteResponse{JobID: job.ID, Status: "running", LastEvent: "run.heartbeat"})
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
	if !errors.Is(err, ErrUncertain) || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

// A run stream that stops emitting must not pin the worker for the whole run
// budget: the idle deadline cuts the hop and the job goes uncertain.
func TestRunOutcomeStreamIdleDeadlineCancels(t *testing.T) {
	s, job := streamJob(t)
	t.Setenv("HUB_STREAM_IDLE", "50ms")
	release := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		_ = json.NewEncoder(w).Encode(streamEvent(job))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer api.Close()
	defer close(release)
	start := time.Now()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s, Limit: 30 * time.Second}).RunOutcome(context.Background(), job)
	if err == nil || outcome.Status != "uncertain" {
		t.Fatalf("silent stream outcome=%+v err=%v", outcome, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("idle stream pinned the worker %v", elapsed)
	}
}

func TestHTTPRunnerConsumesDurableStream(t *testing.T) {
	s, job := streamJob(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Accept") != hubruntime.RunStreamContentType {
			t.Error("stream not requested")
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		event := streamEvent(job)
		_ = json.NewEncoder(w).Encode(event)
		event.Status, event.EventID, event.LastEvent, event.Text = "completed", "terminal:completed", "run.completed", "answer"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
	if err != nil || outcome.Text != "answer" || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	if delivery, err := s.ClaimDelivery(); err != nil || delivery == nil || delivery.Text != "answer" {
		t.Fatalf("delivery=%+v err=%v", delivery, err)
	}
}

func TestAdmittedRestartResumesWithoutSecondSubmission(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	var err error
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimJob()
	if err != nil || recovered == nil {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path != "/v1/resume" {
			t.Errorf("second submission: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
		event.RuntimeGeneration = "gen-2"
		event.EventID = "observed:gen-2"
		_ = json.NewEncoder(w).Encode(event)
		event.Status, event.EventID, event.LastEvent = "interrupted", "terminal:interrupted", "run.interrupted"
		_ = json.NewEncoder(w).Encode(event)
	}))
	defer api.Close()
	outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), *recovered)
	if err == nil || outcome.Status != "interrupted" || outcome.RunID != "run-1" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	m, _, err := s.Mapping(job.ID)
	if err != nil || m.RuntimeGeneration != "gen-2" || m.Status != "interrupted" {
		t.Fatalf("mapping=%+v err=%v", m, err)
	}
}

func TestStreamDisconnectAndStaleFramesKeepOutcomeUncertain(t *testing.T) {
	for _, mode := range []string{"disconnect", "other-job", "other-generation", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, job := streamJob(t)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", hubruntime.RunStreamContentType)
				event := streamEvent(job)
				_ = json.NewEncoder(w).Encode(event)
				if mode == "disconnect" {
					return
				}
				if mode == "malformed" {
					_, _ = w.Write([]byte("not-json\n"))
					return
				}
				event.EventID = "terminal"
				event.Status = "completed"
				event.LastEvent = "run.completed"
				event.Text = "untrusted answer"
				if mode == "other-job" {
					event.JobID = "another"
				} else {
					event.RuntimeGeneration = "stale"
				}
				_ = json.NewEncoder(w).Encode(event)
			}))
			defer api.Close()
			outcome, err := (HTTPRunner{URL: api.URL, Auth: "secret", Spool: s}).RunOutcome(context.Background(), job)
			if err == nil || outcome.Status != "uncertain" {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
			if delivery, err := s.ClaimDelivery(); err != nil || delivery != nil {
				t.Fatalf("invalid final projected: %+v err=%v", delivery, err)
			}
		})
	}
}

func TestStreamReceiptPersistenceFailureAndRecoveryAuthority(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	if err := os.Remove(filepath.Join(s.root, "events")); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err == nil {
		t.Fatal("missing journal ignored")
	}
	m, _, _ := s.Mapping(job.ID)
	if m.EventPosition != 0 || m.RunID != "" {
		t.Fatalf("failed persistence advanced state: %+v", m)
	}
	if err := os.Mkdir(filepath.Join(s.root, "events"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	bad := event
	bad.RunID = "another"
	if err := s.RebindObservation(job, bad); err == nil {
		t.Fatal("recovery changed run")
	}
	event.RuntimeGeneration = "gen-2"
	if err := s.RebindObservation(job, event); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", entries[0].Name()), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, event); err == nil {
		t.Fatal("corrupt receipt ignored")
	}
}

// spool/events is shared: stream receipts, slack dedup markers and event
// subscription states coexist under it. Restart recovery must read only
// receipt-shaped records and leave the other contracts to their owners.
func TestRecoverStreamEventsIgnoresForeignEventFiles(t *testing.T) {
	s, job := streamJob(t)
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", "lv-evt.json"), []byte(`{"schedule_id":"lv-evt","seen":["a1"],"notify_count":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.root, "events", "slack-marker.json"), []byte(`{"provider":"slack","event_id":"ev1"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSpool(s.root); err != nil {
		t.Fatalf("restart rejected shared events dir: %v", err)
	}
}

func TestRecoveryRejectsCrossAudienceReceipt(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal", "answer"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "events"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		path := filepath.Join(s.root, "events", entry.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var receipt streamReceipt
		if err := json.Unmarshal(b, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Delivery == nil {
			continue
		}
		receipt.Delivery.ChatID = 22
		receipt.Delivery.ConversationID = "telegram-22"
		receipt.Delivery.DeliveryTargetID = "telegram-22"
		if err := atomicJSON(path, receipt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewSpool(s.root); err == nil {
		t.Fatal("cross-audience receipt recovered")
	}
}

func TestRecoveryRepairsReceiptBeforeTerminalMappingAndClaim(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	event.Status, event.LastEvent, event.EventID, event.Text = "completed", "run.completed", "terminal", "answer"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	// Restore the mapping to its pre-terminal state: the terminal receipt is the
	// only surviving evidence of a crash before mapping/outbox handoff.
	m, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.Status, m.EventPosition, m.Result = "running", 1, ""
	if err := atomicJSON(s.mappingPath(job.ID), m); err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimJob(); err != nil || claimed != nil {
		t.Fatalf("terminal job rerun: %+v err=%v", claimed, err)
	}
	m, _, err = s.Mapping(job.ID)
	if err != nil || m.Status != "completed" || m.Result != "answer" {
		t.Fatalf("mapping=%+v err=%v", m, err)
	}
}

// Approval requests reach Telegram as inline buttons: one durable callback
// record per sanitized choice so a tap resolves to (job, request, choice)
// even across restarts, and callback data fits Telegram's 64-byte limit.
func TestApprovalRequestDeliversInlineButtons(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:b", "b"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil {
		t.Fatalf("no approval delivery: %+v %v", delivery, err)
	}
	if len(delivery.Buttons) != 2 {
		t.Fatalf("buttons: %+v", delivery.Buttons)
	}
	seen := map[string]bool{}
	for _, button := range delivery.Buttons {
		if button.Text == "" || len(button.Data) > 64 || !strings.HasPrefix(button.Data, "a:") {
			t.Fatalf("invalid button: %+v", button)
		}
		jobID, requestID, choice, err := s.ApprovalCallback(button.Data)
		if err != nil || jobID != job.ID || requestID != "b" || seen[choice] {
			t.Fatalf("callback does not resolve: %q %q %q %v", jobID, requestID, choice, err)
		}
		seen[choice] = true
	}
	if !seen["once"] || !seen["deny"] {
		t.Fatalf("choices lost: %v", seen)
	}
	// Callback records outlive the in-memory spool.
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	if _, requestID, choice, err := reopened.ApprovalCallback(delivery.Buttons[0].Data); err != nil || requestID != "b" || choice != "once" {
		t.Fatalf("callback lost across restart: %q %q %v", requestID, choice, err)
	}
	if _, _, _, err := reopened.ApprovalCallback("a:ffffffffffffffffffffffff"); err == nil {
		t.Fatal("forged callback resolved")
	}
}

// A delivery carrying buttons goes out as an inline keyboard, not plain text.
func TestDeliverOneSendsApprovalKeyboard(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	s, job := streamJob(t)
	g.spool = s
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:b", "b"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.keyboards) != 1 || len(fake.keyboards[0]) != 2 || !strings.HasPrefix(fake.keyboards[0][0].Data, "a:") {
		t.Fatalf("keyboard lost: %+v (plain sent: %v)", fake.keyboards, fake.sent)
	}
}

// Tapping an inline button resolves the durable callback record and applies
// the approval exactly like /approve — verified sender, job-bound, idempotent.
func TestApprovalCallbackQueryAppliesChoice(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "approve-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "key", ChatID: 11, Text: "hello"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:c", "c"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := g.spool.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	delivery, err := g.spool.ClaimDelivery()
	if err != nil || delivery == nil || len(delivery.Buttons) != 2 {
		t.Fatalf("no approval keyboard: %+v %v", delivery, err)
	}
	tap := func(updateID int, sender int64, data string) error {
		return g.handleUpdate(context.Background(), Update{UpdateID: updateID, CallbackQuery: &CallbackQuery{ID: "cb", From: &TGUser{ID: sender}, Message: &Message{Chat: TGChat{ID: 11, Type: "private"}}, Data: data}})
	}
	if err := tap(1, 99, delivery.Buttons[0].Data); err != nil {
		t.Fatal(err)
	}
	mapping, _, err := g.spool.Mapping(job.ID)
	if err != nil || mapping.Control != nil {
		t.Fatalf("stranger applied approval: %+v %v", mapping.Control, err)
	}
	if err := tap(2, 11, delivery.Buttons[0].Data); err != nil {
		t.Fatal(err)
	}
	mapping, _, err = g.spool.Mapping(job.ID)
	if err != nil || mapping.Control == nil || mapping.Control.Choice != "once" {
		t.Fatalf("approval not applied: %+v %v", mapping.Control, err)
	}
	if err := tap(3, 11, delivery.Buttons[0].Data); err != nil {
		t.Fatal(err)
	}
	mapping, _, _ = g.spool.Mapping(job.ID)
	if mapping.Control.Choice != "once" {
		t.Fatal("second tap changed the answer")
	}
	if err := tap(4, 11, "a:000000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	mapping, _, _ = g.spool.Mapping(job.ID)
	if mapping.Control.Choice != "once" {
		t.Fatal("forged callback changed the answer")
	}
	if len(fake.callbackAnswers) < 4 {
		t.Fatalf("unanswered callbacks: %v", fake.callbackAnswers)
	}
}

func TestApprovalIdentityAndDeadlineSurviveRestart(t *testing.T) {
	s, job := streamJob(t)
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:a", "a"
	event.ApprovalChoices = []string{"once", "session", "always", "deny", "injected-choice"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	before, _, err := s.Mapping(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	s, err = NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := s.Mapping(job.ID)
	if err != nil || after.ApprovalState != "pending" || after.ApprovalID != "a" || !after.ApprovalDeadline.Equal(before.ApprovalDeadline) || len(after.ApprovalChoices) != 4 {
		t.Fatalf("approval lost or expanded: %+v err=%v", after, err)
	}
	event.Status, event.LastEvent, event.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	after, _, err = s.Mapping(job.ID)
	if err != nil || after.ApprovalState != "closed" {
		t.Fatalf("terminal run left actionable approval: %+v err=%v", after, err)
	}
}

// The approval prompt must name the flagged action and its deadline; a bare
// "confirm action" forces a blind decision on a two-minute clock.
func TestApprovalRequestDeliveryShowsSummaryAndDeadline(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	s, job := streamJob(t)
	g.spool = s
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:b", "b"
	event.ApprovalChoices = []string{"once", "deny"}
	event.ApprovalSummary = "Write to protected agent-instruction file(s): AGENTS.md."
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(fake.sent) == 0 || !strings.Contains(fake.sent[0], "AGENTS.md") {
		t.Fatalf("approval text lacks action context: %v", fake.sent)
	}
	if !strings.Contains(fake.sent[0], "UTC") {
		t.Fatalf("approval text lacks deadline: %q", fake.sent[0])
	}
}

// A run cancelled after its approval deadline must tell the user WHY — the
// confirmation expired — instead of a bare "cancelled".
func TestExpiredApprovalCancelReportsReason(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	s, job := streamJob(t)
	g.spool = s
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:b", "b"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	m, err := s.loadMappingLocked(job.ID)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	m.ApprovalDeadline = time.Now().Add(-time.Minute)
	if err := s.writeMappingLocked(m); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	cancel := streamEvent(job)
	cancel.Status, cancel.LastEvent, cancel.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, cancel); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		g.deliverOne(context.Background())
	}
	if got := strings.Join(fake.sent, "\n"); !strings.Contains(got, "истекл") {
		t.Fatalf("expired approval reported bare cancel: %q", got)
	}
}

// A run cancelled on an explicit deny says the confirmation was declined,
// not that the task failed on its own.
func TestDeniedApprovalCancelReportsReason(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api = fake
	s, job := streamJob(t)
	g.spool = s
	event := streamEvent(job)
	event.Status, event.LastEvent, event.EventID, event.ApprovalID = "waiting_for_approval", "approval.request", "approval:b", "b"
	event.ApprovalChoices = []string{"once", "deny"}
	if err := s.RecordStreamEvent(job, event); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	m, err := s.loadMappingLocked(job.ID)
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	m.ApprovalDeadline = time.Now().Add(-time.Minute)
	m.Control = &PendingControl{Caller: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), Action: "approve", RequestID: "b", Choice: "deny", State: "closed"}
	if err := s.writeMappingLocked(m); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	cancel := streamEvent(job)
	cancel.Status, cancel.LastEvent, cancel.EventID = "cancelled", "run.cancelled", "terminal:cancelled"
	if err := s.RecordStreamEvent(job, cancel); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		g.deliverOne(context.Background())
	}
	if got := strings.Join(fake.sent, "\n"); !strings.Contains(got, "отклонено") {
		t.Fatalf("denied approval reported bare cancel: %q", got)
	}
}

// A write-ahead receipt can outlive the terminal write it predates: the
// process died between the receipt and the outcome handoff, or the runtime
// kept streaming after the supervisor settled the job. Replay must skip the
// straggler instead of rewinding recorded history or resending its stale
// status — otherwise every restart dies on "job already terminal".
func TestRecoverySkipsStaleTerminalReceipt(t *testing.T) {
	s, job := streamJob(t)
	if _, err := s.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordStreamEvent(job, streamEvent(job)); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, Status: "completed", Text: "saved answer"}); err != nil {
		t.Fatal(err)
	}
	mapping, found, err := s.Mapping(job.ID)
	if err != nil || !found {
		t.Fatalf("mapping found=%v err=%v", found, err)
	}
	stale := streamEvent(job)
	stale.EventID, stale.LastEvent, stale.Text = "tool:late", "tool.end", "stale status"
	straggler := streamReceipt{JobID: job.ID, Position: mapping.EventPosition + 1, Event: stale, Delivery: &Delivery{ID: "job-" + job.ID + "-status", Channel: job.Channel, ConversationID: mapping.ConversationID, DeliveryTargetID: mapping.DeliveryTargetID, ChatID: job.ChatID, Text: "stale status"}}
	settled := streamEvent(job)
	settled.Status, settled.LastEvent, settled.EventID, settled.Text = "completed", "run.completed", "terminal:completed", "saved answer"
	consistent := streamReceipt{JobID: job.ID, Position: mapping.EventPosition + 2, Event: settled}
	if err := atomicJSON(filepath.Join(s.root, "events", "straggler.json"), straggler); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(s.root, "events", "settled.json"), consistent); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(s.root)
	if err != nil {
		t.Fatalf("replay of a stale receipt killed recovery: %v", err)
	}
	mapping, _, err = reopened.Mapping(job.ID)
	if err != nil || mapping.Status != "completed" || mapping.Result != "saved answer" {
		t.Fatalf("terminal history moved: %+v %v", mapping, err)
	}
	if mapping.EventPosition != consistent.Position {
		t.Fatalf("consistent receipt did not apply past the straggler: position=%d", mapping.EventPosition)
	}
	if _, err := os.Stat(filepath.Join(s.root, "outbox", "pending", spoolFileID(straggler.Delivery.ID)+".json")); !os.IsNotExist(err) {
		t.Fatal("stale delivery was re-enqueued")
	}
}
