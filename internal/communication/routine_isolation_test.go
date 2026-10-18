package communication

// CHG-0118 phase-0 regressions: schedule-record isolation, the blocked
// schedule state, the RoutineDraft boundary, the broker credential gate and
// durable Slack event marking.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func dueSchedule(id string, caller identity.Envelope, expr string) Schedule {
	return Schedule{ScheduleID: id, Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: expr, Input: "work", JobKind: "agent"}
}

// An unreadable schedule record must not abort the tick: it is quarantined
// and every other owner's schedule still fires.
func TestTickSchedulesQuarantinesUnreadableRecord(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(dueSchedule("zz-valid", caller, "every:1m"), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	due := created.NextDue
	bad := filepath.Join(s.root, "schedules", "aa-corrupt.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	err = s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence)
	if err == nil {
		t.Fatal("corrupt record reported a clean tick")
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "schedules", "quarantine", "aa-corrupt.json")); statErr != nil {
		t.Fatalf("corrupt record not quarantined: %v", statErr)
	}
	if _, statErr := os.Stat(bad); !os.IsNotExist(statErr) {
		t.Fatal("corrupt record left in the hot directory")
	}
	item, loadErr := s.loadScheduleLocked("zz-valid")
	if loadErr != nil || item.LastOccurrenceID == "" || !item.NextDue.After(due) {
		t.Fatalf("valid schedule did not tick past the corrupt record: %+v %v", item, loadErr)
	}
}

// A failing tick stage must not cancel later stages: durable occurrences
// still dispatch even when the schedule pass fails.
func TestProactiveStagesContinueAfterFailure(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	o.DueAt = time.Now().UTC().Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	// Removing the schedules directory makes TickSchedules fail its ReadDir;
	// DispatchDueOccurrences does not depend on it for this occurrence.
	if err := os.RemoveAll(filepath.Join(s.root, "schedules")); err != nil {
		t.Fatal(err)
	}
	g.tickProactive(time.Now().UTC())
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(s.root, "occurrences", entry.Name()))
		var live RoutineOccurrence
		if json.Unmarshal(b, &live) == nil && live.State == "dispatched" {
			return
		}
	}
	t.Fatal("dispatch stage did not run after the schedule stage failed")
}

// A schedule the tick can never advance becomes visibly blocked and the
// owner gets exactly one notification per cause.
func TestTickBlocksUnreadableScheduleState(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	item, err := s.CreateSchedule(dueSchedule("broken", caller, "every:1m"), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	item.LastOccurrenceID = "not a spool id"
	if err := atomicJSON(s.schedulePath(item.ScheduleID), item); err != nil {
		t.Fatal(err)
	}
	now := item.NextDue.Add(time.Second)
	if err := s.TickSchedules(now, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.loadScheduleLocked("broken")
	if err != nil || blocked.BlockedReason == "" || blocked.BlockedAt.IsZero() {
		t.Fatalf("schedule not marked blocked: %+v %v", blocked, err)
	}
	if routineState(blocked) != "blocked" {
		t.Fatalf("routine state is %q", routineState(blocked))
	}
	delivery, err := s.ClaimDelivery()
	if err != nil || delivery == nil || delivery.ChatID != 11 || delivery.Text == "" {
		t.Fatalf("owner notification missing: %+v %v", delivery, err)
	}
	if err := s.CompleteDelivery(delivery.ID); err != nil {
		t.Fatal(err)
	}
	// A second tick neither re-notifies nor unblocks on its own.
	if err := s.TickSchedules(now.Add(time.Minute), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if extra, err := s.ClaimDelivery(); err != nil || extra != nil {
		t.Fatalf("blocked schedule notified twice: %+v %v", extra, err)
	}
}

// Occurrence authorization failures block the schedule instead of silently
// spinning on the same due instant every five seconds.
func TestTickBlocksOnOccurrenceAuthorizationFailure(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(dueSchedule("denied", caller, "every:1m"), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	deny := func(Job) error { return errors.New("owner unavailable") }
	if err := s.TickSchedules(created.NextDue.Add(time.Second), "", deny); err != nil {
		t.Fatal(err)
	}
	item, err := s.loadScheduleLocked("denied")
	if err != nil || item.BlockedReason == "" {
		t.Fatalf("authorization failure did not block: %+v %v", item, err)
	}
	_ = g
}

// A dispatch-time denial only blocks the occurrence, not the schedule:
// occurrence records can be replayed from a stale state, and a stale record
// must never poison a healthy routine. Persistent owner-policy denial is
// caught one stage earlier, when TickSchedules mints the next occurrence.
func TestDispatchDenialBlocksOccurrenceNotSchedule(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(dueSchedule("dispatch-denied", caller, "every:1m"), caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "dispatch-denied"
	o.DueAt = time.Now().UTC().Add(-time.Minute)
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(time.Now().UTC(), func(Job) error { return errors.New("policy changed") }); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if err := json.Unmarshal(body, &saved); err != nil || saved.State != "blocked" {
		t.Fatalf("occurrence not blocked: %s %v", body, err)
	}
	item, err := s.loadScheduleLocked("dispatch-denied")
	if err != nil || item.BlockedReason != "" {
		t.Fatalf("stale occurrence poisoned the schedule: %+v %v", item, err)
	}
	if d, err := s.ClaimDelivery(); err != nil || d != nil {
		t.Fatalf("dispatch denial produced an owner notice: %+v %v", d, err)
	}
	_ = g
}

// The block notice is routed to the owner's verified home, never to the
// schedule's own destination — a stale topic binding is exactly what may
// have caused the block.
func TestBlockNoticeGoesToOwnerHomeNotDestination(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	sched := dueSchedule("home-notice", caller, "every:1m")
	sched.TopicID = 5
	sched.TaskID = "task-in-topic"
	if _, err := s.CreateSchedule(sched, caller, ""); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	item, err := s.loadScheduleLocked("home-notice")
	if err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	err = s.blockScheduleLocked(&item, "owner authorization failed", time.Now().UTC())
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("owner was not notified of the block: %+v %v", d, err)
	}
	if d.TopicID != 0 || d.TaskID != "" || d.ChatID != 11 || d.DeliveryTargetID != "telegram-11" {
		t.Fatalf("block notice routed to the broken destination: %+v", d)
	}
	_ = g
}

// The legacy credential-store env knobs are fully inert: credential intake
// is broker-only, so neither env nor yaml config may open a local store —
// no ciphertext file and no companion audit ledger is ever created, even
// without a broker configured.
func TestConfigFromEnvIgnoresLegacyCredentialStore(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "credentials", "store.enc")
	t.Setenv("TELEGRAM_ALLOWED_USERS", "11")
	t.Setenv("TELEGRAM_BOT_TOKEN", "bot")
	t.Setenv("HUB_CREDENTIAL_STORE", storePath)
	t.Setenv("HUB_CREDENTIAL_KEY_FILE", filepath.Join(dir, "credential.key"))
	t.Setenv("HUB_AUDIT_LEDGER", filepath.Join(dir, "credentials", "audit.jsonl"))
	yamlPath := filepath.Join(dir, "gw.yaml")
	body := "organization_id: acme\nspool_dir: " + filepath.ToSlash(filepath.Join(dir, "spool")) + "\nusers:\n  - id: me\n    enabled: true\n    state_dir: " + filepath.ToSlash(filepath.Join(dir, "st")) + "\n    workspace_dir: " + filepath.ToSlash(filepath.Join(dir, "ws")) + "\n"
	if err := os.WriteFile(yamlPath, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_COMMUNICATION_CONFIG", yamlPath)
	c, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"st", "ws"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	_ = g
	for _, path := range []string{storePath, filepath.Join(dir, "credentials", "audit.jsonl")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy credential surface materialized: %s", path)
		}
	}

	// The single-space env branch ignores the same knobs identically.
	if err := os.Unsetenv("HUB_COMMUNICATION_CONFIG"); err != nil {
		t.Fatal(err)
	}
	stateDir, workDir := filepath.Join(dir, "s-state"), filepath.Join(dir, "s-ws")
	for _, path := range []string{stateDir, workDir} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HUB_STATE", stateDir)
	t.Setenv("HUB_WORKSPACE", workDir)
	t.Setenv("HUB_COMMUNICATION_SPOOL", filepath.Join(dir, "s-spool"))
	c, err = ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	g, err = New(c)
	if err != nil {
		t.Fatal(err)
	}
	_ = g
	for _, path := range []string{storePath, filepath.Join(dir, "credentials", "audit.jsonl")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("legacy credential surface materialized: %s", path)
		}
	}
}

// A failed Slack ingest must not mark the event seen: Slack retries rejected
// deliveries, and a premature marker drops the event on the floor. The
// marker is written only after the job (or denial reply) is durable.
func TestSlackEventMarkedOnlyAfterDurableIngest(t *testing.T) {
	c := testConfig(t)
	c.SlackSigningSecret = "slack-secret"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	g.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	event := []byte(`{"type":"event_callback","team_id":"TTEAM","event_id":"EvRetry","event":{"type":"message","user":"UUSER","text":"hello slack","channel":"D1","channel_type":"im"}}`)
	post := func() *httptest.ResponseRecorder {
		ts := strconv.FormatInt(g.now().Unix(), 10)
		req := httptest.NewRequest(http.MethodPost, "/v1/slack/events", bytes.NewReader(event))
		req.Header.Set("X-Slack-Signature", slackSig(c.SlackSigningSecret, ts, event))
		req.Header.Set("X-Slack-Request-Timestamp", ts)
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, req)
		return rec
	}
	// Unmapped sender: ingest is denied, the event is not durably consumed.
	if rec := post(); rec.Code != http.StatusForbidden {
		t.Fatalf("unmapped sender: %d", rec.Code)
	}
	// The mapping arrives later; the retry must ingest, not dedupe away.
	g.slackUsers[SlackLink{TeamID: "tteam", UserID: "uuser"}.Key()] = c.Users[0]
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("retry after failed ingest was deduped: %d", rec.Code)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.Channel != "slack_app" {
		t.Fatalf("retried event produced no job: %+v %v", job, err)
	}
	// A third post is a real duplicate now.
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("post-ingest retry rejected: %d", rec.Code)
	}
	if extra, _ := g.spool.ClaimJob(); extra != nil {
		t.Fatalf("duplicate created a second job: %+v", extra)
	}
}

// POST /v1/routines decodes into a draft: lifecycle, identity and routing
// internals sent by the client are ignored, not persisted.
func TestRoutineDraftDropsInternalFields(t *testing.T) {
	g, _, a, _ := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{
		"schedule_id":"draft-guard","destination_task_id":%q,"timezone":"UTC",
		"expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture",
		"revision":99,"enabled":false,"paused":true,
		"blocked_reason":"forged","last_occurrence_id":"forged-occ",
		"snoozed_until":"2099-06-01T00:00:00Z","created_at":"2001-01-01T00:00:00Z",
		"user_id":"mallory","actor_id":"mallory","scope_id":"user:mallory",
		"organization_id":"evil-corp","native_cron_ref":"cron/9",
		"task_id":"unbound-task","envelope":{"principal_id":"mallory"}
	}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var got Schedule
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Paused || got.Revision != 1 || got.BlockedReason != "" || got.LastOccurrenceID != "" || !got.SnoozedUntil.IsZero() || got.NativeCronRef != "" {
		t.Fatalf("client-set lifecycle fields persisted: %+v", got)
	}
	if got.UserID != "alice" || got.ActorID != "alice" || got.ScopeID != "user:alice" || got.OrganizationID == "evil-corp" || got.Envelope.PrincipalID != "alice" {
		t.Fatalf("client-set identity fields persisted: %+v", got)
	}
	if got.TaskID != a.TaskID {
		t.Fatalf("unbound task_id bypassed destination binding: %+v", got)
	}
	if got.CreatedAt.Year() == 2001 {
		t.Fatal("client-set created_at persisted")
	}
}

// Blocked schedules skip the tick entirely; a successful edit clears the
// block and the routine fires again.
func TestBlockedScheduleSkipsAndResumesOnEdit(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	created, err := s.CreateSchedule(dueSchedule("resume-me", caller, "every:1m"), caller, "")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	item, _ := s.loadScheduleLocked("resume-me")
	item.BlockedReason = "test block"
	item.BlockedAt = time.Now().UTC()
	if err := atomicJSON(s.schedulePath(item.ScheduleID), item); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	s.mu.Unlock()
	next := item.NextDue
	if err := s.TickSchedules(next.Add(time.Hour), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	still, _ := s.loadScheduleLocked("resume-me")
	if !still.NextDue.Equal(next) || still.LastOccurrenceID != "" {
		t.Fatalf("blocked schedule advanced: %+v", still)
	}
	patch := map[string]json.RawMessage{"input": json.RawMessage(`"fixed work"`)}
	edited, err := s.editRoutine("resume-me", caller, patch, false, time.Now().UTC(), routineEditContext{User: User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}})
	if err != nil {
		t.Fatal(err)
	}
	if edited.BlockedReason != "" || !edited.BlockedAt.IsZero() {
		t.Fatalf("edit did not clear the block: %+v", edited)
	}
	fire := created.NextDue.Add(2 * time.Hour)
	if err := s.TickSchedules(fire, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	live, _ := s.loadScheduleLocked("resume-me")
	if live.LastOccurrenceID == "" {
		t.Fatal("resumed schedule did not fire")
	}
}
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
	job := Job{Envelope: u.Envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, Trigger: "cron", Text: "synthetic"}
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
	job := Job{Envelope: u.Envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, Trigger: "cron", Text: "synthetic"}
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
	caller := u.Envelope(11)
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
	job := Job{Envelope: u.Envelope(11), UserID: u.ID, ActorID: u.ID, ScopeID: "user:" + u.ID, OrganizationID: c.OrganizationID, Channel: "telegram_bot", ChatID: 11, Text: "boom"}
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
