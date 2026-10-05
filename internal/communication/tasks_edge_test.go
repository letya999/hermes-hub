package communication

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// writeRawTask stores a task record verbatim, bypassing store validation, so
// tests can stage malformed or foreign records that the API never emits.
func writeRawTask(t *testing.T, s *Spool, task Task) {
	t.Helper()
	body, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.taskPath(task.PrincipalID, task.TaskID), body, 0600); err != nil {
		t.Fatal(err)
	}
}

func rawTask(user User, taskID string, chatID int64) Task {
	return Task{Schema: 1, TaskID: taskID, Name: "task-" + taskID, PrincipalID: user.ID, ContextID: user.ID,
		Channel: "telegram_bot", ChatID: chatID, ConversationID: taskConversation(chatID, taskID),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
}

func TestTaskStoreRejectsCorruptAndForeignRecords(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	spool := g.spool

	// Corrupt task record: unparseable file and a foreign-principal record
	// both fail as invalid, never silently coerce into a live task.
	if err := os.WriteFile(spool.taskPath(user.ID, "t-corrupt"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.TaskByID(user, 11, "t-corrupt"); err == nil {
		t.Fatal("corrupt task record resolved")
	}
	// A record stored under alice's key but claiming another principal is
	// rejected by the load-time owner check.
	foreign := rawTask(user, "t-foreign", 11)
	foreign.PrincipalID = "mallory"
	body, err := json.Marshal(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool.taskPath(user.ID, "t-foreign"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.TaskByID(user, 11, "t-foreign"); err == nil {
		t.Fatal("foreign task record accepted for alice")
	}

	// Archived task is readable only for audit, never as a live task.
	archived := rawTask(user, "t-archived", 11)
	archived.Archived = true
	writeRawTask(t, spool, archived)
	if _, err := spool.TaskByID(user, 11, "t-archived"); err == nil {
		t.Fatal("archived task resolved as live")
	}
	if _, err := spool.RenameTask(user, 11, "t-archived", "new-name"); err == nil {
		t.Fatal("archived task mutated")
	}

	// A stale topic binding pointing at an archived task falls through to the
	// current pointer instead of resurrecting it.
	if err := os.WriteFile(spool.topicTaskPath(user.ID, 11, 31), []byte(`{"task_id":"t-archived"}`), 0600); err != nil {
		t.Fatal(err)
	}
	task, err := spool.ResolveTask(user, 11, 31, false)
	if err != nil || task.TaskID != defaultTaskID || task.TopicID != 0 {
		t.Fatalf("archived topic binding resurrected task: %+v err=%v", task, err)
	}

	// Corrupt durable pointers surface as errors instead of guessing a task.
	if err := os.WriteFile(spool.currentTaskPath(user.ID, 11), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ResolveTask(user, 11, 0, false); err == nil {
		t.Fatal("corrupt current pointer resolved")
	}
	if got := g.sessionsCommand(user, 11); !strings.Contains(got, "Не удалось") {
		t.Fatalf("/sessions hid corrupt pointer: %q", got)
	}
	if _, err := spool.UseTask(user, 11, "default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool.topicTaskPath(user.ID, 11, 33), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ResolveTask(user, 11, 33, true); err == nil {
		t.Fatal("corrupt topic binding resolved")
	}
	if err := os.Remove(spool.topicTaskPath(user.ID, 11, 33)); err != nil {
		t.Fatal(err)
	}
}

func TestTaskStoreDuplicateAmbiguousAndBoundRules(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	spool := g.spool

	if _, created, err := spool.CreateTask(user, 11, 0, "alpha", true); err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	// Re-using a live name with makeCurrent just switches to it.
	reused, created, err := spool.CreateTask(user, 11, 0, "alpha", true)
	if err != nil || created || reused.Name != "alpha" {
		t.Fatalf("re-create: %+v %v %v", reused, created, err)
	}

	// A bound topic is exclusive: a second task cannot claim it.
	bound, created, err := spool.CreateTask(user, 11, 7, "bound", false)
	if err != nil || !created {
		t.Fatalf("bind create: %v %v", created, err)
	}
	if _, _, err := spool.CreateTask(user, 11, 7, "squatter", false); err == nil {
		t.Fatal("bound topic accepted a second task")
	}

	// Two distinct records carrying the same name make lookup ambiguous.
	for _, taskID := range []string{"t-dup-1", "t-dup-2"} {
		dup := rawTask(user, taskID, 11)
		dup.Name = "dupname"
		writeRawTask(t, spool, dup)
	}
	if _, err := spool.UseTask(user, 11, "dupname"); err == nil {
		t.Fatal("ambiguous name resolved")
	}

	// A foreign-chat task cannot be mutated from another chat.
	scoped, _, err := spool.CreateTask(user, 44, 0, "scoped", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.SetTaskStyle(user, 11, scoped.TaskID, "x"); err == nil {
		t.Fatal("foreign chat mutated task")
	}
	if _, err := spool.ArchiveTask(user, 11, scoped.TaskID); err == nil {
		t.Fatal("foreign chat archived task")
	}

	// The default task is never archived.
	if _, err := spool.ArchiveTask(user, 11, defaultTaskID); err == nil {
		t.Fatal("default task archived")
	}

	// Archiving the current topic-bound task releases both pointers.
	current, _, err := spool.CreateTask(user, 11, 21, "current-topic", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.ArchiveTask(user, 11, current.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spool.topicTaskPath(user.ID, 11, 21)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("topic binding leaked: %v", err)
	}
	if _, err := os.Stat(spool.currentTaskPath(user.ID, 11)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current pointer leaked: %v", err)
	}
	resolved, err := spool.ResolveTask(user, 11, 0, false)
	if err != nil || resolved.TaskID != defaultTaskID {
		t.Fatalf("archived current did not fall back: %+v err=%v", resolved, err)
	}

	// Conversation ownership still resolves for continuation callers.
	byConv := spool.TaskByConversation(user, 11, bound.ConversationID)
	if byConv.TaskID != bound.TaskID {
		t.Fatalf("conversation resolved wrong task: %+v", byConv)
	}
	// Unknown conversations and foreign principals fall back to default.
	if got := spool.TaskByConversation(user, 11, "telegram-999-t-x"); got.TaskID != defaultTaskID {
		t.Fatalf("unknown conversation resolved %q", got.TaskID)
	}
}

func TestTaskListSkipsInvalidRows(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	spool := g.spool

	// Invalid rows in the tasks directory are skipped, not fatal.
	if err := os.WriteFile(spool.taskPath(user.ID, "t-garbage"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(spool.root, "tasks", "stray-dir"), 0700); err != nil && !os.IsExist(err) {
		t.Fatal(err)
	}
	live, _, err := spool.CreateTask(user, 11, 0, "live", false)
	if err != nil {
		t.Fatal(err)
	}
	// A materialized default participates in the listing.
	if _, err := spool.SetTaskStyle(user, 11, defaultTaskID, "x"); err != nil {
		t.Fatal(err)
	}
	tasks, err := spool.ListTasks(user, 11)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, task := range tasks {
		seen[task.TaskID] = true
	}
	if !seen[defaultTaskID] || !seen[live.TaskID] {
		t.Fatalf("list=%v", seen)
	}
}

func TestStyleForRunErrorBranches(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")
	spool := g.spool

	// Legacy jobs without a task binding carry no style.
	if style, err := spool.StyleForRun(Job{ID: "legacy", Text: "x"}); err != nil || style != "" {
		t.Fatalf("style=%q err=%v", style, err)
	}
	// A task-scoped job without a durable mapping cannot be trusted.
	if _, err := spool.StyleForRun(Job{ID: "no-mapping", TaskID: "t-x", PrincipalID: user.ID}); err == nil {
		t.Fatal("unmapped task job admitted style")
	}
	// A task deleted after admission degrades to no style, not an error.
	orphan, _, err := spool.CreateTask(user, 11, 0, "orphan", false)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: user.envelope(11), ID: "orphan-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "orphan-job", ChatID: 11, TaskID: orphan.TaskID, Text: "x"}
	if _, err := spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(spool.taskPath(user.ID, orphan.TaskID)); err != nil {
		t.Fatal(err)
	}
	if style, err := spool.StyleForRun(job); err != nil || style != "" {
		t.Fatalf("orphan task job: style=%q err=%v", style, err)
	}
	// A corrupt mapping fails closed. Kept last: the poisoned record would
	// break a later Enqueue's idempotency scan of the mappings directory.
	if err := os.WriteFile(spool.taskPath(user.ID, "t-corrupt-style"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool.mappingPath("bad-map"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := spool.StyleForRun(Job{ID: "bad-map", TaskID: "t-corrupt-style", PrincipalID: user.ID}); err == nil {
		t.Fatal("corrupt mapping admitted")
	}
}

func TestTaskCommandEdgeBranches(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}

	if got := g.newTaskCommand(user, 11, 0, "/new bad\x00name"); !strings.Contains(got, "отклонена") {
		t.Fatalf("invalid name accepted: %q", got)
	}
	// A nameless /new creates an auto-named session.
	if got := g.newTaskCommand(user, 11, 0, "/new"); !strings.Contains(got, "создана") {
		t.Fatalf("auto create wrong: %q", got)
	}
	if got := g.newTaskCommand(user, 11, 0, "/new alpha"); !strings.Contains(got, "создана") {
		t.Fatalf("create=%q", got)
	}
	if got := g.newTaskCommand(user, 11, 0, "/new alpha"); !strings.Contains(got, "уже есть") {
		t.Fatalf("reuse=%q", got)
	}
	if got := g.useCommand(user, 11, "/use"); !strings.Contains(got, "/use <id|имя|default>") {
		t.Fatalf("use hint=%q", got)
	}
	if got := g.useCommand(user, 11, "/use missing-task"); !strings.Contains(got, "не найдена") {
		t.Fatalf("use missing=%q", got)
	}
	if got := g.deleteCommand(user, 11, task, "/delete"); !strings.Contains(got, "Не удалось удалить") {
		t.Fatalf("default delete=%q", got)
	}

	// Status inside a bound topic reports the topic and its session.
	bound, _, err := g.spool.CreateTask(user, 11, 51, "bound-status", false)
	if err != nil {
		t.Fatal(err)
	}
	envelope := user.envelope(11)
	envelope.ConversationID = bound.ConversationID
	job := Job{Envelope: envelope, ID: "status-job", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "status-job", ChatID: 11, TaskID: bound.TaskID, Text: "x"}
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RecordOutcome("status-job", RunOutcome{SessionID: "sess-topic", Status: "completed", Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	status := g.taskStatus(user, bound)
	if !strings.Contains(status, "Топик #51") || !strings.Contains(status, "sess-topic") {
		t.Fatalf("status=%q", status)
	}
	// /sessions lists the bound topic marker too.
	list := g.sessionsCommand(user, 11)
	if !strings.Contains(list, "топик #51") {
		t.Fatalf("list=%q", list)
	}
}

func TestUsageCommandSameSessionBranch(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	runner := &usageStubRunner{report: hubruntime.SessionUsage{
		SessionID: "sess-1", DeclaredSessionID: "sess-1", SessionFound: true,
		Source: "hermes-session",
	}}
	g.api, g.runner = &fakeAPI{}, runner
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	got := g.usageCommand(context.Background(), user, 11, task)
	if !strings.Contains(got, "Сессия: sess-1.") || strings.Contains(got, "продолжение") {
		t.Fatalf("same-session report=%q", got)
	}
}
