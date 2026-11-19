package automation

import (
	"context"
	"encoding/json"
	"github.com/letya999/hermes-hub/internal/commops"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

func TestLegacyBindingsReadInPlaceAndEnforceOwner(t *testing.T) {
	root := t.TempDir()
	b := legacyBindings{LegacySpoolReader: commops.LegacySpoolReader{Root: root}}
	for _, dir := range []string{"tasks/topic", "mappings"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alice := commodel.Task{TaskID: "task-a", PrincipalID: "alice", ContextID: "ctx-a", Channel: "telegram_bot", ChatID: 11, TopicID: 5}
	bob := commodel.Task{TaskID: "task-b", PrincipalID: "bob", ContextID: "ctx-b", Channel: "telegram_bot", ChatID: 22, TopicID: 6}
	write(b.taskPath("alice", alice.TaskID), alice)
	write(b.taskPath("bob", bob.TaskID), bob)
	write(b.topicPath("alice", 11, 5), struct {
		TaskID string `json:"task_id"`
	}{alice.TaskID})
	write(b.topicPath("bob", 22, 6), struct {
		TaskID string `json:"task_id"`
	}{bob.TaskID})
	mapping := commodel.JobMapping{JobID: "interactive-a", PrincipalID: "alice", TaskID: alice.TaskID}
	write(filepath.Join(root, "mappings", mapping.JobID+".json"), mapping)
	if got, err := b.loadTask("alice", alice.TaskID); err != nil || got.PrincipalID != "alice" {
		t.Fatalf("owner task: %+v, %v", got, err)
	}
	if tasks, err := b.listTasks("alice"); err != nil || len(tasks) != 1 || tasks[0].TaskID != alice.TaskID {
		t.Fatalf("owner listing: %+v, %v", tasks, err)
	}
	if _, err := b.loadTask("alice", bob.TaskID); err == nil {
		t.Fatal("cross-owner task found")
	}
	if id, err := b.topicTask("alice", 11, 5); err != nil || id != alice.TaskID {
		t.Fatalf("topic binding: %q, %v", id, err)
	}
	if _, err := b.topicTask("alice", 22, 6); err == nil {
		t.Fatal("cross-owner topic found")
	}
	if got, err := b.loadMapping(mapping.JobID); err != nil || got.PrincipalID != "alice" {
		t.Fatalf("origin mapping: %+v, %v", got, err)
	}
	write(b.taskPath("alice", alice.TaskID), bob)
	if _, err := b.loadTask("alice", alice.TaskID); err == nil {
		t.Fatal("rebound task accepted")
	}
	write(b.taskPath("alice", alice.TaskID), alice)
	if err := os.Remove(b.taskPath("alice", alice.TaskID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b.taskPath("bob", bob.TaskID), b.taskPath("alice", alice.TaskID)); err == nil {
		if _, err := b.loadTask("alice", alice.TaskID); err == nil {
			t.Fatal("task symlink followed")
		}
	}
}

func TestHostCreateScheduleResolvesCommunicationOriginWithoutCopy(t *testing.T) {
	h := newHarness(t)
	h.grant(t, "alice", "write")
	h.grant(t, "alice", "read")
	legacy := filepath.Join(h.root, "legacy")
	for _, dir := range []string{"tasks/topic", "mappings"} {
		if err := os.MkdirAll(filepath.Join(legacy, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	b := legacyBindings{LegacySpoolReader: commops.LegacySpoolReader{Root: legacy}}
	user := commodel.User{ID: "alice", Enabled: true, RuntimeID: "rt-alice", PolicyVersion: "policy-1", TelegramIDs: []int64{11}}
	auth := identity.TelegramEnvelope("alice", 11, "rt-alice", "policy-1")
	task := commodel.Task{TaskID: "task-origin", PrincipalID: "alice", ContextID: auth.ContextID, Channel: "telegram_bot", ChatID: 11, TopicID: 5, ConversationID: commodel.TaskConversation(11, "task-origin")}
	write := func(path string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(b.taskPath("alice", task.TaskID), task)
	write(b.topicPath("alice", 11, 5), struct {
		TaskID string `json:"task_id"`
	}{task.TaskID})
	job := commodel.Job{ID: "origin-job", Envelope: auth, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, TopicID: 5, TaskID: task.TaskID, Trigger: "message", IdempotencyKey: "origin-job"}
	job.ConversationID = task.ConversationID
	write(filepath.Join(legacy, "mappings", job.ID+".json"), commodel.MappingFromJob(job, time.Now().UTC()))
	host := h.host(t)
	host.cfg.LegacySpoolDir = legacy
	host.cfg.RoutingUser = func(principal string) (commodel.User, error) {
		if principal != "alice" {
			return commodel.User{}, errUnauthorized
		}
		return user, nil
	}
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := commops.ControlClient{URL: server.URL, Token: "control"}
	var destinations routines.RoutineDestinationPage
	if err := client.Call(context.Background(), auth, "schedule.destinations", map[string]any{"limit": 20}, &destinations); err != nil || len(destinations.Items) != 2 || destinations.Items[1].TaskID != task.TaskID {
		t.Fatalf("service destination listing: %+v, %v", destinations, err)
	}
	in := draft("rt-origin", auth)
	in.OrganizationID = "personal"
	in.OriginJobID, in.DestinationTaskID = job.ID, "current"
	var created commodel.Schedule
	err := client.Call(context.Background(), auth, "schedule.create", map[string]any{"schedule": in}, &created)
	if err != nil {
		t.Fatal(err)
	}
	if created.TaskID != task.TaskID || created.TopicID != task.TopicID || created.OriginJobID != "" {
		t.Fatalf("origin not bound and cleared: %+v", created)
	}
	var replay struct {
		Schedule commodel.Schedule `json:"schedule"`
		Found    bool              `json:"found"`
	}
	if err := client.Call(context.Background(), auth, "schedule.replay", map[string]any{"schedule": in}, &replay); err != nil || !replay.Found || replay.Schedule.ScheduleID != created.ScheduleID {
		t.Fatalf("creation replay: %+v, %v", replay, err)
	}
	write(b.topicPath("alice", 11, 5), struct {
		TaskID string `json:"task_id"`
	}{"other-task"})
	in.ScheduleID = "rt-rebound"
	if err := client.Call(context.Background(), auth, "schedule.create", map[string]any{"schedule": in}, &created); err == nil {
		t.Fatal("rebound topic authorized a new schedule")
	}
	if _, err := os.Stat(filepath.Join(h.root, "users", "alice", "schedules", "rt-rebound.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected schedule persisted: %v", err)
	}
}
