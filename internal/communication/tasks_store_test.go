package communication

import (
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// Direct Spool coverage for the M12 task store: creation, topic binding,
// rename/archive/unarchive/delete paths plus job claim and compaction.
func TestSpoolTaskStoreRoundTrip(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}

	// Empty spool resolves to the implicit default task.
	task, err := s.ResolveTask(user, 11, 0, false)
	if err != nil || task.TaskID != defaultTaskID || task.ConversationID != "telegram-11" {
		t.Fatalf("default resolve: %+v %v", task, err)
	}

	created, fresh, err := s.CreateTask(user, 11, 7, "alpha", true)
	if err != nil || !fresh || created.Name != "alpha" || created.TopicID != 7 {
		t.Fatalf("create: %+v fresh=%v err=%v", created, fresh, err)
	}
	// Same-name create is idempotent and cannot mint a second task.
	again, fresh, err := s.CreateTask(user, 11, 0, "alpha", false)
	if err != nil || fresh || again.TaskID != created.TaskID {
		t.Fatalf("idempotent create: %+v fresh=%v err=%v", again, fresh, err)
	}
	// Topic-bound resolution returns the bound task.
	bound, err := s.ResolveTask(user, 11, 7, false)
	if err != nil || bound.TaskID != created.TaskID {
		t.Fatalf("topic resolve: %+v %v", bound, err)
	}
	// A second task cannot steal the bound topic.
	if _, _, err := s.CreateTask(user, 11, 7, "beta", false); err == nil {
		t.Fatal("topic rebind accepted")
	}

	// adoptTopic turns an unbound topic into a new task named topic-<id>.
	adopted, err := s.ResolveTask(user, 11, 9, true)
	if err != nil || adopted.TopicID != 9 || adopted.Name != "topic-9" {
		t.Fatalf("adopt: %+v %v", adopted, err)
	}
	if err := s.FixTopicPlaceholder(user, 11, 9, "renamed topic"); err != nil {
		t.Fatal(err)
	}
	fixed, err := s.TaskByID(user, 11, adopted.TaskID)
	if err != nil || fixed.Name != "renamed topic" {
		t.Fatalf("placeholder fix: %+v %v", fixed, err)
	}

	// Rebind moves the binding to a free topic; renaming the topic mirrors.
	if err := s.RebindTaskTopic(user, 11, adopted.TaskID, 13); err != nil {
		t.Fatal(err)
	}
	if err := s.RebindTaskTopic(user, 11, adopted.TaskID, 7); err == nil {
		t.Fatal("rebind onto foreign topic accepted")
	}
	if err := s.RenameTopicTask(user, 11, 13, "topic name"); err != nil {
		t.Fatal(err)
	}
	fixed, _ = s.TaskByID(user, 11, adopted.TaskID)
	if fixed.Name != "topic name" {
		t.Fatalf("topic rename mirror: %+v", fixed)
	}

	renamed, err := s.RenameTask(user, 11, created.TaskID, "alpha-2")
	if err != nil || renamed.Name != "alpha-2" || renamed.ConversationID != created.ConversationID {
		t.Fatalf("rename: %+v %v", renamed, err)
	}
	if _, err := s.RenameTask(user, 11, created.TaskID, "bad\nname"); err == nil {
		t.Fatal("control-char name accepted")
	}

	live, err := s.ListTasks(user, 11)
	if err != nil || len(live) != 3 {
		t.Fatalf("list: %v %v", live, err)
	}
	if _, err := s.TaskBySelector(user, 11, "alpha-2"); err != nil {
		t.Fatal(err)
	}
	switched, err := s.UseTask(user, 11, "alpha-2")
	if err != nil || switched.TaskID != created.TaskID {
		t.Fatalf("use: %+v %v", switched, err)
	}

	// Auto title fires once on an auto-named task only.
	auto, fresh, err := s.CreateTask(user, 11, 0, "", false)
	if err != nil || !fresh || !auto.NameAuto {
		t.Fatalf("auto create: %+v %v %v", auto, fresh, err)
	}
	if !s.TaskNameAuto(user.ID, auto.TaskID) || !s.AutoTitleTask(user.ID, auto.TaskID, "auto title") {
		t.Fatal("auto title refused")
	}
	if s.AutoTitleTask(user.ID, auto.TaskID, "again") || s.AutoTitleTask(user.ID, created.TaskID, "nope") {
		t.Fatal("auto title fired twice or on named task")
	}

	// Archive releases the topic binding and hides from live list.
	archived, err := s.ArchiveTask(user, 11, adopted.TaskID)
	if err != nil || !archived.Archived {
		t.Fatalf("archive: %+v %v", archived, err)
	}
	list, err := s.ListArchivedTasks(user, 11)
	if err != nil || len(list) != 1 {
		t.Fatalf("archived list: %v %v", list, err)
	}
	// Unarchive restores the task and rebinds its freed topic.
	restored, rebound, err := s.UnarchiveTask(user, 11, adopted.TaskID)
	if err != nil || !rebound || restored.Archived {
		t.Fatalf("unarchive: %+v rebound=%v %v", restored, rebound, err)
	}
	// Delete by selector removes an archived record for good.
	if _, err := s.ArchiveTask(user, 11, adopted.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteTaskBySelector(user, 11, adopted.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskByID(user, 11, adopted.TaskID); err == nil {
		t.Fatal("deleted task still resolves")
	}
	if _, err := s.DeleteTask(user, 11, "t-absent"); err == nil {
		t.Fatal("missing task deleted")
	}

	// Job lifecycle: idempotent enqueue, claim, complete, compact.
	env := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
	job := Job{Envelope: env, ID: "job-a", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-a", ChatID: 11, Text: "hi", CreatedAt: time.Now().UTC()}
	ok, err := s.Enqueue(job)
	if err != nil || !ok {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	if ok, err := s.Enqueue(job); err != nil || ok {
		t.Fatalf("duplicate enqueue: %v %v", ok, err)
	}
	dup := job
	dup.Text = "different"
	if _, err := s.Enqueue(dup); err == nil {
		t.Fatal("idempotency conflict accepted")
	}
	claimed, err := s.ClaimJob()
	if err != nil || claimed == nil || claimed.ID != "job-a" {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if err := s.CompleteJob("job-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(time.Now().Add(time.Hour), 0, CompactLimits{}); err != nil {
		t.Fatal(err)
	}
}
