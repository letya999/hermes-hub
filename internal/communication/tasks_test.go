package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func taskUpdate(updateID, messageID int, text string, sender int64, topic int64) Update {
	message := &Message{MessageID: messageID, From: &TGUser{ID: sender}, Chat: TGChat{ID: sender, Type: "private"}, Text: text}
	if topic != 0 {
		message.DirectMessagesTopic = &TGDirectTopic{TopicID: topic}
	}
	return Update{UpdateID: updateID, Message: message}
}

func TestMessageTopicIDParsesDirectTopic(t *testing.T) {
	if got := messageTopicID(&Message{}); got != 0 {
		t.Fatalf("root topic=%d", got)
	}
	if got := messageTopicID(&Message{DirectMessagesTopic: &TGDirectTopic{TopicID: 42}}); got != 42 {
		t.Fatalf("topic=%d", got)
	}
	var update Update
	if err := json.Unmarshal([]byte(`{"update_id":1,"message":{"message_id":2,"from":{"id":11},"chat":{"id":11,"type":"private"},"text":"hi","direct_messages_topic":{"topic_id":7}}}`), &update); err != nil {
		t.Fatal(err)
	}
	if got := messageTopicID(update.Message); got != 7 {
		t.Fatalf("wire topic=%d", got)
	}
}

func TestTaskLifecycleCommands(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	send := func(id int, text string) {
		if err := g.handleUpdate(ctx, taskUpdate(id, id, text, 11, 0)); err != nil {
			t.Fatal(err)
		}
	}
	send(1, "/task new alpha")
	send(2, "/task")
	send(3, "/tasks")
	send(4, "/task rename beta")
	send(5, "/style коротко и по делу")
	send(6, "/task")
	send(7, "/task archive")
	send(8, "/task")
	for range 12 {
		g.deliverOne(ctx)
	}
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "Задача создана и выбрана") || !strings.Contains(joined, "переименована: beta") || !strings.Contains(joined, "Стиль задачи beta") || !strings.Contains(joined, "в архиве") {
		t.Fatalf("sent=%v", fake.sent)
	}
	if _, err := g.spool.TaskByID(user, 11, "t-missing"); err == nil {
		t.Fatal("foreign task id resolved")
	}
	tasks, err := g.spool.ListTasks(user, 11)
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != defaultTaskID {
		t.Fatalf("archived task listed: %v", tasks)
	}
}

func TestTaskJobsGetOwnConversationAndTopicDelivery(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &fakeRunner{}
	g.api, g.runner = fake, runner
	ctx := context.Background()

	// A text post inside an unbound DM topic adopts it as a new task.
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "что тут нового", 11, 42)); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatalf("job=%v err=%v", job, err)
	}
	if job.TopicID != 42 || job.TaskID == "" || job.TaskID == defaultTaskID {
		t.Fatalf("job binding missing: %+v", job)
	}
	if job.ConversationID != taskConversation(11, job.TaskID) {
		t.Fatalf("conversation=%q", job.ConversationID)
	}
	if job.DeliveryTargetID != "telegram-11" {
		t.Fatalf("audience rewritten: %+v", job.Envelope)
	}
	response, err := g.runner.Run(ctx, *job, g.user(job.UserID))
	if err != nil {
		t.Fatal(err)
	}
	_ = g.spool.EnqueueDelivery(Delivery{ID: "job-" + job.ID + "-response", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, ConversationID: job.ConversationID, DeliveryTargetID: job.DeliveryTargetID, Text: response, CreatedAt: g.now().UTC()})
	_ = g.spool.CompleteJob(job.ID)
	g.deliverOne(ctx)
	if len(fake.topics) != 1 || fake.topics[0] != 42 {
		t.Fatalf("delivery landed outside the topic: %v", fake.topics)
	}

	// A second post in the same topic reuses the bound task session.
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "ещё вопрос", 11, 42)); err != nil {
		t.Fatal(err)
	}
	job2, err := g.spool.ClaimJob()
	if err != nil || job2 == nil {
		t.Fatalf("job2=%v err=%v", job2, err)
	}
	if job2.ConversationID != job.ConversationID || job2.TaskID != job.TaskID {
		t.Fatal("topic post did not reuse the bound task")
	}
	_ = g.spool.CompleteJob(job2.ID) // Context serialization: release before the next claim.

	// A root-DM post follows the current pointer (the just-created task) and
	// inherits the task's bound topic: a topic-bound task's replies live in
	// its topic no matter where the message arrived.
	if err := g.handleUpdate(ctx, taskUpdate(3, 3, "корневой вопрос", 11, 0)); err != nil {
		t.Fatal(err)
	}
	job3, err := g.spool.ClaimJob()
	if err != nil || job3 == nil {
		t.Fatalf("job3=%v err=%v", job3, err)
	}
	if job3.TaskID != job.TaskID || job3.TopicID != 42 {
		t.Fatalf("root post did not follow current pointer: %+v", job3)
	}
}

func TestTaskCommandsDoNotAdoptUnboundTopic(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/task", 11, 9)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "default") {
		t.Fatalf("command adopted a topic: %v", fake.sent)
	}
	if len(fake.topics) != 1 || fake.topics[0] != 9 {
		t.Fatalf("command reply left the topic: %v", fake.topics)
	}
}

func TestTaskExplicitBindAndWrongTopicGuards(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	// /task new inside a topic binds it explicitly.
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/task new work", 11, 5)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	bound, err := g.spool.ResolveTask(user, 11, 5, false)
	if err != nil || bound.TaskID == defaultTaskID {
		t.Fatalf("topic bind failed: %+v %v", bound, err)
	}
	// A second explicit bind of the same topic is refused by name uniqueness
	// but a different name reports the conflict.
	if _, _, err := g.spool.CreateTask(user, 11, 5, "other", true); err == nil {
		t.Fatal("topic rebind accepted")
	}
	// A job claiming a foreign chat's task is rejected at admission.
	foreign := Job{Envelope: user.envelope(11), ID: "foreign", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", ChatID: 999, TaskID: bound.TaskID, Text: "x"}
	if _, err := g.spool.Enqueue(foreign); err == nil {
		t.Fatal("foreign-chat task binding admitted")
	}
	if _, err := g.spool.Enqueue(Job{Envelope: user.envelope(11), ID: "missing-task", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "cron", ChatID: 11, TaskID: "t-absent", Text: "x"}); err == nil {
		t.Fatal("missing task binding admitted")
	}
}

func TestStyleSnapshotPinnedPerJobAndIsolated(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	task, created, err := g.spool.CreateTask(user, 11, 0, "styled", false)
	if err != nil || !created {
		t.Fatalf("create: %v %v", task, err)
	}
	if _, err := g.spool.SetTaskStyle(user, 11, task.TaskID, "laconic"); err != nil {
		t.Fatal(err)
	}
	envelope := user.envelope(11)
	envelope.ConversationID = task.ConversationID
	job := Job{Envelope: envelope, ID: "style-job-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "style-job-1", ChatID: 11, TaskID: task.TaskID, Text: "hi"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	style, err := g.spool.StyleForRun(job)
	if err != nil || style != "laconic" {
		t.Fatalf("style=%q err=%v", style, err)
	}
	// A later style change must not rewrite the admitted job's snapshot.
	if _, err := g.spool.SetTaskStyle(user, 11, task.TaskID, "verbose"); err != nil {
		t.Fatal(err)
	}
	style, err = g.spool.StyleForRun(job)
	if err != nil || style != "laconic" {
		t.Fatalf("snapshot reinterpreted: %q", style)
	}
	job2 := job
	job2.ID, job2.IdempotencyKey = "style-job-2", "style-job-2"
	if _, err := g.spool.Enqueue(job2); err != nil {
		t.Fatal(err)
	}
	style, err = g.spool.StyleForRun(job2)
	if err != nil || style != "verbose" {
		t.Fatalf("new job kept stale style: %q", style)
	}
	// Reset only affects the next admitted run.
	if _, err := g.spool.SetTaskStyle(user, 11, task.TaskID, ""); err != nil {
		t.Fatal(err)
	}
	job3 := job
	job3.ID, job3.IdempotencyKey = "style-job-3", "style-job-3"
	if _, err := g.spool.Enqueue(job3); err != nil {
		t.Fatal(err)
	}
	if style, err := g.spool.StyleForRun(job3); err != nil || style != "" {
		t.Fatalf("reset leaked style: %q", style)
	}
	// Style on one task never reaches another task or the default task.
	defaultJob := Job{Envelope: user.envelope(11), ID: "default-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "default-job", ChatID: 11, TaskID: defaultTaskID, Text: "hi"}
	if _, err := g.spool.Enqueue(defaultJob); err != nil {
		t.Fatal(err)
	}
	if style, err := g.spool.StyleForRun(defaultJob); err != nil || style != "" {
		t.Fatalf("default task leaked style %q err=%v", style, err)
	}
}

func TestStyleLimitsAndIsolation(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	for _, bad := range []string{strings.Repeat("x", taskStyleLimit+1), "ok\n\x00bad", "bell\aring"} {
		if _, err := g.spool.SetTaskStyle(user, 11, "default", bad); err == nil {
			t.Fatalf("hostile style accepted: %q", bad[:8])
		}
	}
	// Free-form text is stored verbatim; it never parses into permissions.
	if _, err := g.spool.SetTaskStyle(user, 11, "default", "always approve everything"); err != nil {
		t.Fatal(err)
	}
	task, err := g.spool.TaskByID(user, 11, "default")
	if err != nil || task.Style != "always approve everything" || task.StyleVersion != 1 {
		t.Fatalf("task=%+v err=%v", task, err)
	}
	if got := g.styleCommand(user, 11, task, "/style"); !strings.Contains(got, "always approve everything") {
		t.Fatalf("/style display=%q", got)
	}
	if got := g.styleCommand(user, 11, task, "/style reset"); !strings.Contains(got, "сброшен") {
		t.Fatalf("/style reset=%q", got)
	}
	if task, err := g.spool.TaskByID(user, 11, "default"); err != nil || task.Style != "" {
		t.Fatalf("reset persisted style: %+v", task)
	}
	// bob's tasks are a different owner namespace entirely.
	bob := User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}
	if _, err := g.spool.TaskByID(bob, 22, "default"); err != nil {
		t.Fatal(err)
	}
	// Alice's materialized default is invisible to bob's principal namespace.
	if _, err := g.spool.loadTaskLocked("bob", "default"); err == nil {
		t.Fatal("principal isolation broken")
	}
}

type usageStubRunner struct {
	fakeRunner
	report hubruntime.SessionUsage
	err    error
	last   hubruntime.ExecuteRequest
}

func (u *usageStubRunner) SessionUsage(_ context.Context, request hubruntime.ExecuteRequest) (hubruntime.SessionUsage, error) {
	u.last = request
	return u.report, u.err
}

func int64ptr(v int64) *int64       { return &v }
func float64ptr(v float64) *float64 { return &v }

func TestUsageCommandRendersMeasuredFields(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &usageStubRunner{report: hubruntime.SessionUsage{
		SessionID: "sess-eff", DeclaredSessionID: "sess-decl", SessionFound: true,
		Model: "gpt-test", InputTokens: int64ptr(120), OutputTokens: int64ptr(45),
		CacheReadTokens: int64ptr(7), CacheWriteTokens: int64ptr(3), ReasoningTokens: int64ptr(9),
		APICallCount: int64ptr(4), MessageCount: int64ptr(6), ToolCallCount: int64ptr(2),
		EstimatedCostUSD: float64ptr(0.0123), Compactions: int64ptr(1),
		LastCompactionAt: "2026-01-01T00:00:00Z", Source: "hermes-session",
	}}
	g.api, g.runner = fake, runner
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	got := g.usageCommand(context.Background(), user, 11, task)
	for _, want := range []string{"sess-eff", "sess-decl", "gpt-test", "unknown", "120", "45", "0.0123", "hermes-session", "2026-01-01"} {
		if !strings.Contains(got, want) {
			t.Fatalf("/usage missing %q: %s", want, got)
		}
	}
	if runner.last.Envelope.ConversationID != task.ConversationID {
		t.Fatalf("usage asked for wrong conversation: %s", runner.last.Envelope.ConversationID)
	}

	// Context is only rendered when both measured values are authoritative.
	runner.report.ContextTokens = int64ptr(50)
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "Контекст запроса: unknown") {
		t.Fatalf("half-known context leaked: %s", got)
	}
	runner.report.ContextWindow = int64ptr(200)
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "50/200 (25%)") {
		t.Fatalf("measured context missing: %s", got)
	}

	runner.report = hubruntime.SessionUsage{DeclaredSessionID: "sess-decl", Source: "hermes-session"}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "ещё не создана") {
		t.Fatalf("missing session not reported: %s", got)
	}
	runner.err = errors.New("no runtime")
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "недоступно") {
		t.Fatalf("runner error not reported: %s", got)
	}
	runner.err = nil
	g.runner = &fakeRunner{}
	if got := g.usageCommand(context.Background(), user, 11, task); !strings.Contains(got, "недоступно для этого исполнителя") {
		t.Fatalf("unsupported runner not reported: %s", got)
	}
}

func TestTaskUseByNameSwitchesCurrent(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")
	if _, _, err := g.spool.CreateTask(user, 11, 0, "alpha", true); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.UseTask(user, 11, "default"); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/task use alpha", 11, 0)); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "/task use отсутствует", 11, 0)); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		g.deliverOne(ctx)
	}
	if len(fake.sent) < 2 || !strings.Contains(fake.sent[0], "alpha") || !strings.Contains(fake.sent[1], "не найдена") {
		t.Fatalf("replies=%v", fake.sent)
	}
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil || task.TaskID == "default" {
		t.Fatalf("current task did not switch: %+v %v", task, err)
	}
}

func TestTaskCommandRejectsBadInput(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/task use":     "Используйте /task use",
		"/task rename":  "Используйте /task rename",
		"/task bogus":   "Используйте /task",
		"/task archive": "Не удалось архивировать",
	}
	for text, want := range cases {
		if got := g.taskCommand(user, 11, 0, task, text); !strings.Contains(got, want) {
			t.Fatalf("%q => %q, want %q", text, got, want)
		}
	}
	if got := g.styleCommand(user, 11, task, "/style \x07"); !strings.Contains(got, "Стиль отклонён") {
		t.Fatalf("invalid style accepted: %q", got)
	}
}

func TestTaskTopicAdoptionConflictAndArchive(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	// adopt=true on an unbound topic materializes a task bound to it.
	bound, err := g.spool.ResolveTask(user, 11, 77, true)
	if err != nil {
		t.Fatal(err)
	}
	if bound.TopicID != 77 || bound.TaskID == defaultTaskID {
		t.Fatalf("adoption failed: %+v", bound)
	}
	again, err := g.spool.ResolveTask(user, 11, 77, false)
	if err != nil || again.TaskID != bound.TaskID {
		t.Fatalf("topic binding lost: %+v %v", again, err)
	}
	// A second task cannot claim an already-bound topic.
	if _, _, err := g.spool.CreateTask(user, 11, 77, "squatter", true); err == nil {
		t.Fatal("topic squat accepted")
	}
	// A foreign chat cannot mutate the task.
	if _, err := g.spool.RenameTask(user, 999, bound.TaskID, "stolen"); err == nil {
		t.Fatal("foreign-chat rename accepted")
	}
	if _, err := g.spool.SetTaskStyle(user, 999, bound.TaskID, "x"); err == nil {
		t.Fatal("foreign-chat style accepted")
	}
	// Conversation → task lookup keeps continuation replies in the topic.
	if found := g.spool.TaskByConversation(user, 11, bound.ConversationID); found.TaskID != bound.TaskID || found.TopicID != 77 {
		t.Fatalf("conversation lookup lost task: %+v", found)
	}
	if found := g.spool.TaskByConversation(user, 11, "conv-elsewhere"); found.TaskID != defaultTaskID {
		t.Fatalf("unknown conversation leaked a task: %+v", found)
	}
	// /tasks lists topic-bound tasks with their topic number.
	if list := g.tasksCommand(user, 11); !strings.Contains(list, "топик #77") {
		t.Fatalf("topic missing from list: %s", list)
	}
	// Archiving unbinds the topic; the topic resolves back to default.
	if _, err := g.spool.ArchiveTask(user, 11, bound.TaskID); err != nil {
		t.Fatal(err)
	}
	fallback, err := g.spool.ResolveTask(user, 11, 77, false)
	if err != nil || fallback.TaskID != defaultTaskID {
		t.Fatalf("archived task still bound: %+v %v", fallback, err)
	}
}

func TestHTTPRunnerSessionUsagePostsEnvelope(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody hubruntime.UsageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(hubruntime.SessionUsage{SessionFound: true, SessionID: "s-1", Model: "m", Source: "hermes_session_db"})
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL, Auth: "secret"}
	request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{ConversationID: "conv", ContextID: "ctx"}}
	report, err := runner.SessionUsage(context.Background(), request)
	if err != nil || !report.SessionFound || report.SessionID != "s-1" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if gotPath != "/v1/usage" || gotAuth != "Bearer secret" {
		t.Fatalf("wire=%s %s", gotPath, gotAuth)
	}
	if gotBody.Envelope.ConversationID != "conv" || gotBody.Envelope.ContextID != "ctx" {
		t.Fatalf("envelope lost: %+v", gotBody.Envelope)
	}
}

func TestHTTPRunnerSessionUsageSurfacesErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer server.Close()
	runner := HTTPRunner{URL: server.URL}
	request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{ConversationID: "conv"}}
	if _, err := runner.SessionUsage(context.Background(), request); err == nil {
		t.Fatal("HTTP error swallowed")
	}
	if _, err := (HTTPRunner{URL: "http://127.0.0.1:1"}).SessionUsage(context.Background(), request); err == nil {
		t.Fatal("transport error swallowed")
	}
}

func TestTaskMappingSurvivesSpoolReopen(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "привет", 11, 31)); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil {
		t.Fatal("job missing")
	}
	spool2, err := NewSpool(g.spool.root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := spool2.ResolveTask(g.user("alice"), 11, 31, false)
	if err != nil || task.TaskID != job.TaskID {
		t.Fatalf("binding lost across reopen: %+v %v", task, err)
	}
	if task.ConversationID != job.ConversationID {
		t.Fatal("conversation changed across reopen")
	}
}

func TestScheduleKeepsTaskBinding(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 5, "cron-task", false)
	if err != nil {
		t.Fatal(err)
	}
	caller := user.envelope(11)
	schedule := Schedule{ScheduleID: "cron-1", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, TaskID: task.TaskID, TopicID: 5, Timezone: "UTC", Expression: "* * * * *", Input: "tick", CreatedAt: g.now().UTC()}
	if _, err := g.spool.CreateSchedule(schedule, caller, ""); err != nil {
		t.Fatal(err)
	}
	// Materialize the occurrence exactly as TickSchedules does.
	job := Job{Envelope: schedule.Envelope, OrganizationID: schedule.OrganizationID, UserID: schedule.UserID, ActorID: schedule.ActorID, ScopeID: schedule.ScopeID, Channel: schedule.Channel, ChatID: schedule.ChatID, TaskID: schedule.TaskID, TopicID: schedule.TopicID, Text: schedule.Input, Trigger: "cron"}
	occ := RoutineOccurrence{ScheduleID: schedule.ScheduleID, Revision: 1, DueAt: time.Now().UTC(), Job: job}
	if err := validateOccurrence(occ, caller); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(occ.Job); err != nil {
		t.Fatal(err)
	}
	claimed, err := g.spool.ClaimJob()
	if err != nil || claimed == nil {
		t.Fatal("cron job missing")
	}
	if claimed.ConversationID != task.ConversationID || claimed.TopicID != 5 {
		t.Fatalf("cron job lost task binding: %+v", claimed)
	}
	if claimed.Envelope.ConversationID != task.ConversationID {
		t.Fatal("envelope not rebound to task conversation")
	}
}
