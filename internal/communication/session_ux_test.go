package communication

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

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
	send(1, "/new alpha")
	current, err := g.spool.ResolveTask(user, 11, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.ArchiveTask(user, 11, current.TaskID); err != nil {
		t.Fatal(err)
	}
	archived, err := g.spool.ListArchivedTasks(user, 11)
	if err != nil || len(archived) != 1 || archived[0].Name != "alpha" {
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
	send(2, "/new beta")
	send(3, "/delete")
	if !strings.Contains(fake.sent[len(fake.sent)-1], "удалена") {
		t.Fatalf("delete reply: %v", fake.sent)
	}
	current, err = g.spool.ResolveTask(user, 11, 0, false)
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

func forumUpdate(updateID, messageID int, text string, sender, chatID, threadID int64) Update {
	return Update{UpdateID: updateID, Message: &Message{MessageID: messageID, From: &TGUser{ID: sender}, Chat: TGChat{ID: chatID, Type: "supergroup"}, Text: text, MessageThreadID: threadID, IsTopicMessage: threadID != 0}}
}

func forumOpCalled(ops []forumOp, method string, chat, topic int64, name string) bool {
	for _, op := range ops {
		if op.method == method && op.chat == chat && op.topic == topic && op.name == name {
			return true
		}
	}
	return false
}

// Issue #220: in a forum group where the bot manages topics, /new gives the
// session its own bot-created topic; the confirmation lands inside it and
// posts in that topic route to the session.
func TestNewInForumGroupCreatesTopic(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "/new alpha", 11, -100123, 7)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)

	current, err := g.spool.ResolveTask(user, -100123, 0, false)
	if err != nil || current.Name != "alpha" || current.NameAuto {
		t.Fatalf("named task: %+v %v", current, err)
	}
	if current.TopicID == 0 || current.TopicID == 7 {
		t.Fatalf("task not bound to a bot-created topic: %+v", current)
	}
	if !forumOpCalled(fake.forumOps, "create", -100123, 0, "alpha") {
		t.Fatalf("no createForumTopic call with session name: %+v", fake.forumOps)
	}
	if got := fake.topics[len(fake.topics)-1]; got != current.TopicID {
		t.Fatalf("confirmation landed in topic %d, want %d", got, current.TopicID)
	}
	// A post inside the new topic routes to the bound session.
	if err := g.handleUpdate(ctx, forumUpdate(2, 2, "привет", 11, -100123, current.TopicID)); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.TaskID != current.TaskID {
		t.Fatalf("post in forum topic did not reach the session: %+v %v", job, err)
	}
}

// Issue #220: an auto-named session's topic gets the generated name, and the
// adopted Hermes title later renames the topic.
func TestNewForumTopicFollowsAutoTitle(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &usageStubRunner{report: hubruntime.SessionUsage{SessionFound: true, Title: "Квартальный отчёт", MessageCount: int64ptr(4)}}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "/new", 11, -100123, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	current, err := g.spool.ResolveTask(user, -100123, 0, false)
	if err != nil || !current.NameAuto || current.TopicID == 0 {
		t.Fatalf("auto task in forum: %+v %v", current, err)
	}
	if !forumOpCalled(fake.forumOps, "create", -100123, 0, current.Name) {
		t.Fatalf("topic name must match the task name: %+v", fake.forumOps)
	}
	job := Job{Envelope: user.envelope(11), ID: "j1", UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", ChatID: -100123, TopicID: current.TopicID, TaskID: current.TaskID, Text: "x"}
	g.maybeAutoTitle(ctx, job)
	if !forumOpCalled(fake.forumOps, "edit", -100123, current.TopicID, "Квартальный отчёт") {
		t.Fatalf("forum topic not renamed with adopted title: %+v", fake.forumOps)
	}
}

// Issue #220: without manage_topics the create fails and /new keeps the
// historical behavior — the task binds to the topic it was typed in.
func TestNewForumFallbackBindsInboundTopic(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{forumErr: errors.New("not enough rights")}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "/new beta", 11, -100123, 7)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	current, err := g.spool.ResolveTask(user, -100123, 0, false)
	if err != nil || current.Name != "beta" || current.TopicID != 7 {
		t.Fatalf("fallback binding lost: %+v %v", current, err)
	}
	if got := fake.topics[len(fake.topics)-1]; got != 7 {
		t.Fatalf("fallback reply landed in topic %d, want 7", got)
	}
}

// Issue #220: deleting a session deletes its forum topic; a confirmation
// typed inside the deleted topic is delivered to the group root instead.
func TestDeleteInForumGroupDeletesTopic(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "/new alpha", 11, -100123, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	current, err := g.spool.ResolveTask(user, -100123, 0, false)
	if err != nil || current.TopicID == 0 {
		t.Fatalf("forum task: %+v %v", current, err)
	}
	if err := g.handleUpdate(ctx, forumUpdate(2, 2, "/delete", 11, -100123, current.TopicID)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if !forumOpCalled(fake.forumOps, "delete", -100123, current.TopicID, "") {
		t.Fatalf("forum topic not deleted with the session: %+v", fake.forumOps)
	}
	if got := fake.topics[len(fake.topics)-1]; got != 0 {
		t.Fatalf("confirmation must leave the deleted topic, got %d", got)
	}
}

// RebindTaskTopic refuses a topic owned by a sibling task and moves a task's
// binding to a fresh forum topic.
func TestRebindTaskTopicCollision(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.user("alice")

	bound, _, err := g.spool.CreateTask(user, -100123, 7, "bound", false)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := g.spool.CreateTask(user, -100123, 0, "fresh", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RebindTaskTopic(user, -100123, fresh.TaskID, 7); err == nil {
		t.Fatal("rebind stole a bound topic")
	}
	if err := g.spool.RebindTaskTopic(user, -100123, fresh.TaskID, 42); err != nil {
		t.Fatalf("rebind to free topic: %v", err)
	}
	moved, err := g.spool.TaskByID(user, -100123, fresh.TaskID)
	if err != nil || moved.TopicID != 42 {
		t.Fatalf("rebind result: %+v %v", moved, err)
	}
	resolved, err := g.spool.ResolveTask(user, -100123, 42, false)
	if err != nil || resolved.TaskID != fresh.TaskID {
		t.Fatalf("new topic does not resolve: %+v %v", resolved, err)
	}
	if still, _ := g.spool.TaskByID(user, -100123, bound.TaskID); still.TopicID != 7 {
		t.Fatalf("sibling binding disturbed: %+v", still)
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
	if lines := g.quotaLines(context.Background()); len(lines) != 1 || !strings.Contains(lines[0], "не настроен") {
		t.Fatalf("unconfigured quota status: %v", lines)
	}
	t.Setenv("HUB_CLIPROXY_MGMT_URL", "http://127.0.0.1:1")
	t.Setenv("HUB_CLIPROXY_MGMT_KEY", "k")
	lines := g.quotaLines(context.Background())
	if len(lines) != 1 || !strings.Contains(lines[0], "недоступно") {
		t.Fatalf("failure not reported honestly: %v", lines)
	}
}

func TestQuotaSelectsOnlyActiveAntigravityCredential(t *testing.T) {
	files := []cliproxyAuthFile{
		{Provider: "codex", AuthIndex: "other"},
		{Provider: "antigravity", AuthIndex: "disabled", Disabled: true},
		{Provider: "antigravity", AuthIndex: "own"},
	}
	if selected, ok := pickAuthFile(files, ""); !ok || selected.AuthIndex != "own" {
		t.Fatalf("unexpected default quota credential: %v %v", selected.Provider, ok)
	}
	for _, index := range []string{"other", "disabled", "missing"} {
		if _, ok := pickAuthFile(files, index); ok {
			t.Fatalf("accepted unrelated or disabled credential %q", index)
		}
	}
}

func TestSetMyCommandsRegistersEveryScope(t *testing.T) {
	var mu struct {
		scopes   []string
		commands []map[string]string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bottoken/setMyCommands" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			Commands []map[string]string `json:"commands"`
			Scope    map[string]string   `json:"scope"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.scopes = append(mu.scopes, payload.Scope["type"])
		mu.commands = payload.Commands
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	api := newTelegramAPI(server.URL, "token", time.Second)
	if err := api.SetMyCommands(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"default", "all_private_chats", "all_group_chats", "all_chat_administrators"} {
		if !slices.Contains(mu.scopes, scope) {
			t.Fatalf("scope %s not registered: %v", scope, mu.scopes)
		}
	}
	if len(mu.commands) != len(botCommandInventory) {
		t.Fatalf("menu size %d != inventory %d", len(mu.commands), len(botCommandInventory))
	}
	for i, c := range botCommandInventory {
		if mu.commands[i]["command"] != c.Command || mu.commands[i]["description"] != c.Description {
			t.Fatalf("menu drifted from inventory at %d: %v", i, mu.commands[i])
		}
	}
	// /help renders exactly the registered inventory.
	help := commandHelp()
	for _, c := range botCommandInventory {
		if !strings.Contains(help, "/"+c.Command+" — "+c.Description) {
			t.Fatalf("help missing /%s", c.Command)
		}
	}
}

// Issue #220: a topic a user creates in Telegram becomes a bound session
// named exactly like it — no /new needed and no "topic-<id>" label.
func TestUserCreatedTopicBindsNamedSession(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	upd := forumUpdate(1, 1, "", 11, -100123, 42)
	upd.Message.ForumTopicCreated = &TGForumTopicCreated{Name: "Идеи"}
	if err := g.handleUpdate(ctx, upd); err != nil {
		t.Fatal(err)
	}
	task, err := g.spool.ResolveTask(user, -100123, 42, false)
	if err != nil || task.Name != "Идеи" || task.NameAuto || task.TopicID != 42 {
		t.Fatalf("topic session: %+v %v", task, err)
	}
	// Topic creation alone must not steal the chat's current pointer.
	if current, _ := g.spool.ResolveTask(user, -100123, 0, false); current.TaskID != defaultTaskID {
		t.Fatalf("current pointer stolen by topic creation: %+v", current)
	}
	if err := g.handleUpdate(ctx, forumUpdate(2, 2, "привет", 11, -100123, 42)); err != nil {
		t.Fatal(err)
	}
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.TaskID != task.TaskID {
		t.Fatalf("post in user-created topic missed its session: %+v %v", job, err)
	}
}

// Issue #220: renaming a forum topic renames the bound session, so the
// session list always shows the same name the topic carries.
func TestTopicRenameRenamesBoundSession(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "/new alpha", 11, -100123, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	current, err := g.spool.ResolveTask(user, -100123, 0, false)
	if err != nil || current.TopicID == 0 {
		t.Fatalf("forum task: %+v %v", current, err)
	}
	upd := forumUpdate(2, 2, "", 11, -100123, current.TopicID)
	upd.Message.ForumTopicEdited = &TGForumTopicEdited{Name: "alpha-2"}
	if err := g.handleUpdate(ctx, upd); err != nil {
		t.Fatal(err)
	}
	renamed, err := g.spool.TaskByID(user, -100123, current.TaskID)
	if err != nil || renamed.Name != "alpha-2" || renamed.NameAuto {
		t.Fatalf("session did not follow the topic name: %+v %v", renamed, err)
	}
}

// Issue #220: when a first post adopts a topic before Telegram delivers the
// service message, the real topic name still replaces the "topic-<id>"
// placeholder — while a deliberately named bound session keeps its name.
func TestLateTopicCreatedFixesPlaceholder(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	if err := g.handleUpdate(ctx, forumUpdate(1, 1, "привет", 11, -100123, 42)); err != nil {
		t.Fatal(err)
	}
	adopted, err := g.spool.ResolveTask(user, -100123, 42, false)
	if err != nil || adopted.Name != "topic-42" {
		t.Fatalf("adopted task: %+v %v", adopted, err)
	}
	upd := forumUpdate(2, 2, "", 11, -100123, 42)
	upd.Message.ForumTopicCreated = &TGForumTopicCreated{Name: "Реальное имя"}
	if err := g.handleUpdate(ctx, upd); err != nil {
		t.Fatal(err)
	}
	fixed, err := g.spool.TaskByID(user, -100123, adopted.TaskID)
	if err != nil || fixed.Name != "Реальное имя" || fixed.NameAuto {
		t.Fatalf("placeholder not fixed: %+v %v", fixed, err)
	}
	// A bound task with a real name keeps it on a late service message.
	named, _, err := g.spool.CreateTask(user, -100123, 77, "моё", false)
	if err != nil {
		t.Fatal(err)
	}
	upd = forumUpdate(3, 3, "", 11, -100123, 77)
	upd.Message.ForumTopicCreated = &TGForumTopicCreated{Name: "Телеграмное"}
	if err := g.handleUpdate(ctx, upd); err != nil {
		t.Fatal(err)
	}
	kept, err := g.spool.TaskByID(user, -100123, named.TaskID)
	if err != nil || kept.Name != "моё" {
		t.Fatalf("named task overwritten: %+v %v", kept, err)
	}
}

// A forum service message from a non-configured user must not mint sessions.
func TestForeignForumServiceIgnored(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	user := g.user("alice")

	upd := forumUpdate(1, 1, "", 999, -100123, 43)
	upd.Message.ForumTopicCreated = &TGForumTopicCreated{Name: "Чужой топик"}
	if err := g.handleUpdate(ctx, upd); err != nil {
		t.Fatal(err)
	}
	if task, _ := g.spool.ResolveTask(user, -100123, 43, false); task.TopicID == 43 {
		t.Fatalf("foreign service message bound a session: %+v", task)
	}
}
