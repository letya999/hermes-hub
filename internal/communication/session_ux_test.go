package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestUnknownCommandGetsHelpNotModel(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	for _, text := range []string{"/bogus", "/egress", "/sessionsx"} {
		if err := g.handleUpdate(ctx, taskUpdate(1, 1, text, 11, 0)); err != nil {
			t.Fatal(err)
		}
		g.deliverOne(ctx)
	}
	if job, err := g.spool.ClaimJob(); err != nil || job != nil {
		t.Fatalf("unknown command reached the model: %+v", job)
	}
	for _, sent := range fake.sent {
		if !strings.Contains(sent, "Неизвестная команда") || !strings.Contains(sent, "/usage") {
			t.Fatalf("no help for unknown command: %q", sent)
		}
	}
}

func TestNewAndSessionsAliasesCreateAndList(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/new", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "Задача создана") || !strings.Contains(fake.sent[0], "автоматически") {
		t.Fatalf("auto-named create reply: %v", fake.sent)
	}
	current, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil || !current.NameAuto || current.TaskID == defaultTaskID {
		t.Fatalf("auto task not current: %+v %v", current, err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "/sessions", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	last := fake.sent[len(fake.sent)-1]
	if !strings.Contains(last, "текущая") || !strings.Contains(last, "—") {
		t.Fatalf("session list missing dates/marker: %s", last)
	}
	// /new with an explicit name keeps it and clears NameAuto.
	if err := g.handleUpdate(ctx, taskUpdate(3, 3, "/new работа", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	current, err = g.spool.ResolveTask(user, 11, 0, false)
	if err != nil || current.Name != "работа" || current.NameAuto {
		t.Fatalf("named create: %+v %v", current, err)
	}
}

func TestTaskDeleteAndArchivedList(t *testing.T) {
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
		g.deliverOne(ctx)
	}
	send(1, "/task new alpha")
	send(2, "/task archive")
	send(3, "/task archived")
	joined := strings.Join(fake.sent, "\n")
	if !strings.Contains(joined, "Архив задач:") || !strings.Contains(joined, "alpha") {
		t.Fatalf("archive list: %v", fake.sent)
	}
	archived, err := g.spool.ListArchivedTasks(user, 11)
	if err != nil || len(archived) != 1 {
		t.Fatalf("archived=%v err=%v", archived, err)
	}
	// Deleting an archived task by id removes its record.
	if _, err := g.spool.DeleteTaskBySelector(user, 11, archived[0].TaskID); err != nil {
		t.Fatal(err)
	}
	if archived, _ = g.spool.ListArchivedTasks(user, 11); len(archived) != 0 {
		t.Fatal("archived task survived delete")
	}
	// Deleting the current live task resets the pointer to default.
	send(4, "/task new beta")
	send(5, "/task delete")
	if !strings.Contains(fake.sent[len(fake.sent)-1], "удалена") {
		t.Fatalf("delete reply: %v", fake.sent)
	}
	current, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil || current.TaskID != defaultTaskID {
		t.Fatalf("current after delete: %+v", current)
	}
	if _, err := g.spool.TaskByID(user, 11, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.DeleteTaskBySelector(user, 11, "default"); err == nil {
		t.Fatal("default task deleted")
	}
	if _, err := g.spool.DeleteTaskBySelector(user, 11, "нет-такой"); err == nil {
		t.Fatal("missing task deleted")
	}
}

func TestForumTopicResolvesAndReplies(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()

	groupMessage := &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "привет", MessageThreadID: 55, IsTopicMessage: true}
	if got := messageTopicID(groupMessage); got != 55 {
		t.Fatalf("forum topic=%d", got)
	}
	groupMessage.IsTopicMessage = false
	if got := messageTopicID(groupMessage); got != 0 {
		t.Fatalf("non-topic message bound: %d", got)
	}
	if got := topicField(-100123); got != "message_thread_id" {
		t.Fatalf("group field=%s", got)
	}
	if got := topicField(11); got != "direct_messages_topic_id" {
		t.Fatalf("private field=%s", got)
	}

	// A forum-topic post adopts the topic as a task; the reply lands back in it.
	update := Update{UpdateID: 1, Message: &Message{MessageID: 1, From: &TGUser{ID: 11}, Chat: TGChat{ID: -100123, Type: "supergroup"}, Text: "привет", MessageThreadID: 55, IsTopicMessage: true}}
	if err := g.handleUpdate(ctx, update); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.TopicID != 55 || job.ChatID != -100123 {
		t.Fatalf("forum job: %+v %v", job, err)
	}
	_ = g.spool.EnqueueDelivery(Delivery{ID: "d1", JobID: job.ID, Channel: job.Channel, ChatID: job.ChatID, TaskID: job.TaskID, TopicID: job.TopicID, Text: "ok", CreatedAt: g.now().UTC()})
	_ = g.spool.CompleteJob(job.ID)
	g.deliverOne(ctx)
	if len(fake.topics) != 1 || fake.topics[0] != 55 {
		t.Fatalf("forum reply lost topic: %v", fake.topics)
	}
}

func TestAutoTitleAdoptsSessionTitleOnce(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: true, Title: "Обсуждение квартального отчёта", MessageCount: int64ptr(4)}}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")

	auto, _, err := g.spool.CreateTask(user, 11, 0, "", true)
	if err != nil || !auto.NameAuto {
		t.Fatalf("auto task: %+v %v", auto, err)
	}
	named, _, err := g.spool.CreateTask(user, 11, 0, "ручное", false)
	if err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: user.envelope(11), ID: "j1", UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", ChatID: 11, TaskID: auto.TaskID, Text: "x"}
	g.maybeAutoTitle(ctx, job)
	renamed, err := g.spool.TaskByID(user, 11, auto.TaskID)
	if err != nil || renamed.Name != "Обсуждение квартального отчёта" || renamed.NameAuto {
		t.Fatalf("title not adopted: %+v %v", renamed, err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "автоматически названа") {
		t.Fatalf("no title notice: %v", fake.sent)
	}
	// Named tasks and short sessions are never renamed.
	job.TaskID = named.TaskID
	runner.report.Title = "не трогать"
	g.maybeAutoTitle(ctx, job)
	still, _ := g.spool.TaskByID(user, 11, named.TaskID)
	if still.Name != "ручное" {
		t.Fatalf("explicit name overwritten: %+v", still)
	}
	auto2, _, err := g.spool.CreateTask(user, 11, 0, "", false)
	if err != nil {
		t.Fatal(err)
	}
	runner.report.MessageCount = int64ptr(1)
	job.TaskID = auto2.TaskID
	g.maybeAutoTitle(ctx, job)
	still2, _ := g.spool.TaskByID(user, 11, auto2.TaskID)
	if !still2.NameAuto {
		t.Fatalf("titled too early: %+v", still2)
	}
}

func TestQuotaLinesRendersMeasuredBuckets(t *testing.T) {
	var apiCallBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode(map[string]any{"files": []map[string]any{
				{"provider": "antigravity", "auth_index": "idx-1", "project_id": "proj", "email": "u@example.com"},
			}})
		case "/v0/management/api-call":
			_ = json.NewDecoder(r.Body).Decode(&apiCallBody)
			body := `{"groups":[{"displayName":"Gemini Models","buckets":[{"window":"weekly","resetTime":"2026-10-07T07:22:39Z","remainingFraction":0.924},{"window":"5h","resetTime":"2026-10-05T10:07:37Z","remainingFraction":0.988}]}]}`
			_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 200, "body": body})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUB_CLIPROXY_MGMT_URL", server.URL)
	t.Setenv("HUB_CLIPROXY_MGMT_KEY", "mgmt-test")
	t.Setenv("HUB_CLIPROXY_AUTH_INDEX", "")
	lines := g.quotaLines(context.Background())
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"u@example.com", "Gemini Models", "92.4%", "98.8%", "07.10"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("quota line missing %q: %s", want, joined)
		}
	}
	header, _ := apiCallBody["header"].(map[string]any)
	if header["Authorization"] != "Bearer $TOKEN$" || header["User-Agent"] != "antigravity" {
		t.Fatalf("api-call headers: %v", header)
	}
	// Cache hit: the second call returns the same lines without re-fetching.
	if again := g.quotaLines(context.Background()); strings.Join(again, "\n") != joined {
		t.Fatal("quota cache missed")
	}
}

func TestQuotaLinesDisabledOrFailing(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	if lines := g.quotaLines(context.Background()); lines != nil {
		t.Fatalf("unconfigured quota spoke: %v", lines)
	}
	t.Setenv("HUB_CLIPROXY_MGMT_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_CLIPROXY_MGMT_KEY", "k")
	lines := g.quotaLines(context.Background())
	if len(lines) != 1 || !strings.Contains(lines[0], "недоступно") {
		t.Fatalf("failure not reported honestly: %v", lines)
	}
}
