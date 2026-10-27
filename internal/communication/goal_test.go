package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/letya999/hermes-hub/internal/locale"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// The synthetic home task shares TaskID "default" across every chat — keying
// the record by taskID leaks a goal set in one chat into all the others.
// The conversation id is the unique key (SPEC-0074 R9).
func TestGoalRecordKeyedByConversation(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	goal := Goal{TaskID: "default", PrincipalID: "alice", ConversationID: "telegram-11", Text: "chat 11 goal", Engine: "hub", Status: "active", MaxTurns: 20}
	if err := spool.SaveGoal(goal); err != nil {
		t.Fatalf("SaveGoal: %v", err)
	}
	// Same taskID "default", different chat → must not resolve chat 11's goal.
	if _, found, _ := spool.GoalForTask("alice", "telegram-999"); found {
		t.Fatal("goal keyed by taskID leaks across chats")
	}
	got, found, _ := spool.GoalForTask("alice", "telegram-11")
	if !found || got.Text != "chat 11 goal" {
		t.Fatalf("own conversation must resolve: %+v", got)
	}
}

// SPEC-0074 R6: stored record fields stay bounded — goal text and verdict
// reasons never land at whatever length a caller or judge produced.
func TestGoalRecordBoundsStoredFields(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	goal := Goal{TaskID: "t-1", PrincipalID: "alice", ConversationID: "conv-1",
		Text: strings.Repeat("g", goalTextLimit+64), LastReason: strings.Repeat("r", goalReasonLimit+64),
		Engine: "hub", Status: "active", MaxTurns: 7}
	if err := spool.SaveGoal(goal); err != nil {
		t.Fatalf("SaveGoal: %v", err)
	}
	got, found, _ := spool.GoalForTask("alice", "conv-1")
	if !found || len(got.Text) > goalTextLimit || len(got.LastReason) > goalReasonLimit {
		t.Fatalf("unbounded record: text=%d reason=%d", len(got.Text), len(got.LastReason))
	}
	// A multi-byte boundary must not end the stored field in a split rune —
	// reasons surface in owner notifications.
	if err := spool.SaveGoal(Goal{TaskID: "t-2", PrincipalID: "alice", ConversationID: "conv-2",
		LastReason: "x" + strings.Repeat("я", goalReasonLimit), Engine: "hub", Status: "active"}); err != nil {
		t.Fatalf("SaveGoal utf8: %v", err)
	}
	got2, _, _ := spool.GoalForTask("alice", "conv-2")
	if !utf8.ValidString(got2.LastReason) || len(got2.LastReason) > goalReasonLimit {
		t.Fatalf("stored reason is invalid or unbounded: valid=%v len=%d",
			utf8.ValidString(got2.LastReason), len(got2.LastReason))
	}
}

func TestGoalRecordRoundTripAndIsolation(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	goal := Goal{TaskID: "t-1", PrincipalID: "owner", ConversationID: "conv-1", Text: "ship the api", Engine: "hub", Status: "active", MaxTurns: 7, Epoch: 3}
	if err := spool.SaveGoal(goal); err != nil {
		t.Fatalf("SaveGoal: %v", err)
	}
	got, ok, err := spool.GoalForTask("owner", "conv-1")
	if err != nil || !ok {
		t.Fatalf("GoalForTask: ok=%v err=%v", ok, err)
	}
	if got.Text != "ship the api" || got.Engine != "hub" || got.Status != "active" || got.MaxTurns != 7 || got.Epoch != 3 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if _, ok, err := spool.GoalForTask("owner", "conv-2"); err != nil || ok {
		t.Fatalf("foreign task must not resolve: ok=%v err=%v", ok, err)
	}
	if _, ok, err := spool.GoalForTask("other", "conv-1"); err != nil || ok {
		t.Fatalf("foreign principal must not resolve: ok=%v err=%v", ok, err)
	}
	got.UpdatedAt = time.Time{}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must be stamped on save")
	}
}

// /goal verbs mirror upstream: bare and `status` report, `pause`/`resume`/
// `clear` control, free text sets the goal. Out-of-scope upstream verbs get a
// named refusal rather than silently becoming goal text (SPEC-0074).
func TestGoalArgsParse(t *testing.T) {
	cases := []struct {
		text string
		verb string
		arg  string
	}{
		{"/goal", "status", ""},
		{"/goal status", "status", ""},
		{"/goal pause", "pause", ""},
		{"/goal resume", "resume", ""},
		{"/goal clear", "clear", ""},
		{"/goal ship the api", "set", "ship the api"},
		{"/goal status report draft", "set", "status report draft"},
		{"/goal draft a contract", "unsupported", "draft"},
		{"/goal gate add go test", "unsupported", "gate"},
		{"/goal wait 123", "unsupported", "wait"},
		{"/goal subgoal check tests", "unsupported", "subgoal"},
		{"/goal show", "unsupported", "show"},
		{"/goal done", "unsupported", "done"},
		{"/goal stop", "unsupported", "stop"},
	}
	for _, tc := range cases {
		verb, arg := parseGoalArgs(tc.text)
		if verb != tc.verb || arg != tc.arg {
			t.Fatalf("%q: got (%q, %q) want (%q, %q)", tc.text, verb, arg, tc.verb, tc.arg)
		}
	}
}

func TestGoalClear(t *testing.T) {
	spool, err := NewSpool(filepath.Join(t.TempDir(), "spool"))
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}
	if err := spool.SaveGoal(Goal{TaskID: "t-1", PrincipalID: "owner", ConversationID: "conv-1", Text: "x", Engine: "hub", Status: "active"}); err != nil {
		t.Fatalf("SaveGoal: %v", err)
	}
	if err := spool.ClearGoal("owner", "conv-1"); err != nil {
		t.Fatalf("ClearGoal: %v", err)
	}
	if _, ok, err := spool.GoalForTask("owner", "conv-1"); err != nil || ok {
		t.Fatalf("cleared goal must not resolve: ok=%v err=%v", ok, err)
	}
}

// /goal <text> rides the verified envelope and the task's stable conversation
// to POST /v1/goal — never a run endpoint — then persists the durable record
// and queues the kickoff turn as an ordinary spool job (SPEC-0074).
func TestGoalSetRoutesToRuntimeAndPersists(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	var mu sync.Mutex
	var paths []string
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/goal" {
			t.Errorf("goal reached %s — slash commands must never hit run endpoints", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"engine": "hub", "kickoff_prompt": "Goal: ship the api", "max_turns": 20,
		})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal ship the api", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.sent) < 1 && time.Now().Before(deadline) {
		g.deliverOne(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	if len(paths) != 1 || paths[0] != "/v1/goal" {
		t.Fatalf("paths: %v", paths)
	}
	if body["op"] != "set" || body["text"] != "ship the api" ||
		body["conversation_id"] != task.ConversationID || body["user_id"] != "alice" {
		t.Fatalf("request body: %v", body)
	}
	mu.Unlock()
	goal, ok, err := g.spool.GoalForTask(user.ID, task.ConversationID)
	if err != nil || !ok {
		t.Fatalf("goal record missing: ok=%v err=%v", ok, err)
	}
	if goal.Text != "ship the api" || goal.Engine != "hub" || goal.Status != "active" ||
		goal.MaxTurns != 20 || goal.ConversationID != task.ConversationID || goal.Epoch != 1 {
		t.Fatalf("record: %+v", goal)
	}
	// Kickoff rides the ordinary job path with the goal trigger.
	pending := 0
	var kick Job
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			pending++
			kick = j
		}
	}
	if pending != 1 || !strings.Contains(kick.Text, "ship the api") || kick.TaskID != task.TaskID {
		t.Fatalf("kickoff job: count=%d %+v", pending, kick)
	}
	// The kickoff must be claimable under the record's epoch — an unstamped
	// turn no-ops at claim and the goal silently never starts.
	if kick.GoalEpoch != goal.Epoch || !g.goalTurnClaimable(kick) {
		t.Fatalf("kickoff not claimable: epoch=%d record=%d", kick.GoalEpoch, goal.Epoch)
	}
}

// /goal status answers from the durable record — no model call; a task with
// no goal says so honestly instead of fabricating one.
func TestGoalStatusCommand(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal status", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "Нет активной цели") {
		t.Fatalf("no-goal reply: %v", fake.sent)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship the api", Engine: "hub", Status: "active", TurnsUsed: 3, MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	fake.sent = nil
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "/goal", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "ship the api") ||
		!strings.Contains(fake.sent[0], "3/20") || !strings.Contains(fake.sent[0], "hub") {
		t.Fatalf("status reply: %v", fake.sent)
	}
	if called {
		t.Fatal("status must not call the runtime — the durable record answers")
	}
}

// Hub pause/resume flip the durable record — pausing stops the loop without a
// model call; resuming re-queues the turn as an ordinary spool job.
func TestGoalPauseResumeHub(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal pause", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "paused" {
		t.Fatalf("paused: %+v", goal)
	}
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "/goal resume", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	goal, _, _ = g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "active" {
		t.Fatalf("resumed: %+v", goal)
	}
	if called {
		t.Fatal("hub pause/resume are local record ops — no runtime call")
	}
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	found := false
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			found = true
		}
	}
	if !found {
		t.Fatal("resume must re-queue the goal turn")
	}
}

// /goal status on a native goal refreshes the record from upstream before
// answering — the local row is only a mirror.
func TestGoalNativeStatusMirrorsUpstream(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Engine: "native", Status: "paused", TurnsUsed: 9, MaxTurns: 12}}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "native", Status: "active", MaxTurns: 20, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal status", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	runner.mu.Lock()
	if len(runner.evals) != 1 || runner.evals[0].Op != "status" {
		t.Fatalf("native status must dispatch to runtime: %+v", runner.evals)
	}
	runner.mu.Unlock()
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "paused" || goal.TurnsUsed != 9 || goal.MaxTurns != 12 {
		t.Fatalf("mirrored: %+v", goal)
	}
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "9/12") {
		t.Fatalf("reply: %v", fake.sent)
	}
}

// For a native goal upstream is authoritative: pause/resume/clear ride the
// runtime op so the upstream record moves with ours, and the record mirrors
// the returned state.
func TestGoalNativeControlOpsRouteToRuntime(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Engine: "native", Status: "paused", TurnsUsed: 3, MaxTurns: 5}}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "native", Status: "active", MaxTurns: 20, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal pause", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	runner.mu.Lock()
	if len(runner.evals) != 1 || runner.evals[0].Op != "pause" {
		t.Fatalf("native pause must dispatch to runtime: %+v", runner.evals)
	}
	runner.mu.Unlock()
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "paused" || goal.TurnsUsed != 3 || goal.MaxTurns != 5 {
		t.Fatalf("record must mirror upstream state: %+v", goal)
	}
	runner.result = hubruntime.GoalResult{Engine: "native", Status: "active", TurnsUsed: 3, MaxTurns: 5, ContinuationPrompt: "keep going"}
	if err := g.handleUpdate(ctx, taskUpdate(2, 2, "/goal resume", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	goal, _, _ = g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "active" {
		t.Fatalf("resumed: %+v", goal)
	}
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	found := false
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" && j.Text == "keep going" {
			found = true
		}
	}
	if !found {
		t.Fatal("native resume must queue upstream's continuation prompt")
	}
	runner.result = hubruntime.GoalResult{Engine: "native", Status: "cleared"}
	if err := g.handleUpdate(ctx, taskUpdate(3, 3, "/goal clear", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if _, ok, _ := g.spool.GoalForTask(user.ID, task.ConversationID); ok {
		t.Fatal("record must be gone after native clear")
	}
	runner.mu.Lock()
	ops := []string{}
	for _, e := range runner.evals {
		ops = append(ops, e.Op)
	}
	runner.mu.Unlock()
	if len(ops) != 3 || ops[0] != "pause" || ops[1] != "resume" || ops[2] != "clear" {
		t.Fatalf("ops: %v", ops)
	}
}

// /goal clear drops the durable record so nothing can resume it; pending
// turns still in the spool become stale at claim (SPEC-0074).
func TestGoalClearCommand(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "x", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal clear", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if _, ok, _ := g.spool.GoalForTask(user.ID, task.ConversationID); ok {
		t.Fatal("record must be gone after clear")
	}
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "сброшен") {
		t.Fatalf("clear reply: %v", fake.sent)
	}
}

// After clear the record restarts at epoch 1 — the fresh kickoff must not
// collide with the cleared goal's kickoff still durable in the spool.
func TestGoalSetAfterClearEnqueuesFreshKickoff(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"engine": "hub", "kickoff_prompt": "Goal: x", "max_turns": 20})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	if _, err := g.spool.ResolveTask(user, 11, 0, true); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range []string{"/goal first", "/goal clear", "/goal second"} {
		if err := g.handleUpdate(ctx, taskUpdate(i+1, i+1, cmd, 11, 0)); err != nil {
			t.Fatal(err)
		}
		g.deliverOne(ctx)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(fake.sent) < 3 && time.Now().Before(deadline) {
		g.deliverOne(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if len(fake.sent) != 3 || strings.Contains(fake.sent[2], "недоступна") {
		t.Fatalf("set after clear: %v", fake.sent)
	}
	claims := 0
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			claims++
		}
	}
	if claims != 2 {
		t.Fatalf("kickoffs pending after set/clear/set: %d", claims)
	}
}

// Out-of-scope upstream verbs refuse by name — a mistyped `/goal done` must
// never silently become goal text (SPEC-0074).
func TestGoalUnsupportedVerbRefusal(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	if _, err := g.spool.ResolveTask(g.user("alice"), 11, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal done", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "не поддерживается") ||
		!strings.Contains(fake.sent[0], "done") {
		t.Fatalf("refusal: %v", fake.sent)
	}
	if called {
		t.Fatal("unsupported verb must never reach the runtime")
	}
}

// Re-setting a goal on an active one pins the existing engine (no silent
// engine switch) and bumps the epoch so queued continuations of the previous
// goal become stale (SPEC-0074).
func TestGoalResetBumpsEpochAndPinsEngine(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_ = json.NewEncoder(w).Encode(map[string]any{"engine": "hub", "kickoff_prompt": "Goal: x", "max_turns": 20})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "old", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 4}); err != nil {
		t.Fatal(err)
	}
	// The superseded epoch's kickoff still sits in the spool — the re-set's
	// kickoff must not collide with it.
	if _, err := g.spool.Enqueue(Job{ID: "goal-" + task.TaskID + "-e1", Envelope: user.Envelope(1), OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", IdempotencyKey: "goal-kickoff-" + task.TaskID + "-e1", ChatID: 11, TaskID: task.TaskID, Text: "old prompt", GoalEpoch: 4, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal new objective", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(bodies) < 1 && time.Now().Before(deadline) {
		g.deliverOne(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if len(bodies) != 1 || bodies[0]["expect_engine"] != "hub" {
		t.Fatalf("expect_engine: %v", bodies)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Epoch != 5 || goal.Text != "new objective" {
		t.Fatalf("epoch: %+v", goal)
	}
	// The re-set must queue a fresh claimable kickoff — an ID/idempotency
	// collision with the superseded kickoff would silently drop the turn.
	claims := 0
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" && j.GoalEpoch == 5 {
			claims++
		}
	}
	if claims != 1 {
		t.Fatalf("epoch-5 kickoff jobs pending: %d", claims)
	}
}

// A named runtime refusal (engine_denied, engine_conflict, judge_denied)
// surfaces verbatim and must not leave a half-written record.
func TestGoalSetNamedRefusal(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"failure_class": "engine_conflict"})
	}))
	defer server.Close()
	g.api, g.runner = fake, HTTPRunner{URL: server.URL, Auth: "test"}
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/goal switch engines", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.Contains(fake.sent[0], "другом движке") {
		t.Fatalf("refusal: %v", fake.sent)
	}
	if _, ok, _ := g.spool.GoalForTask(user.ID, task.ConversationID); ok {
		t.Fatal("refused set must not persist a record")
	}
}

// /help advertises the goal command alongside the other session controls.
func TestHelpAdvertisesGoal(t *testing.T) {
	if !strings.Contains(commandHelp(locale.LangRU), "/goal — ") {
		t.Fatal("/goal missing from command help")
	}
}

// A runner that can both run turns and answer goal evaluations.
type goalLoopRunner struct {
	mu     sync.Mutex
	runs   []string
	evals  []hubruntime.GoalRequest
	result hubruntime.GoalResult
}

func (f *goalLoopRunner) Run(_ context.Context, job Job, user User) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, job.ID)
	return "turn answer " + job.ID, nil
}

func (f *goalLoopRunner) Goal(_ context.Context, request hubruntime.GoalRequest) (hubruntime.GoalResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evals = append(f.evals, request)
	if f.result != (hubruntime.GoalResult{}) {
		return f.result, nil
	}
	return hubruntime.GoalResult{Verdict: "continue", Reason: "still working", ContinuationPrompt: "next turn"}, nil
}

// A completed goal turn evaluates the reply through the runtime: a continue
// verdict increments the turn counter and queues the next turn (SPEC-0074).
func TestGoalAfterJobContinueEnqueuesNext(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 3, Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: user.Envelope(11), ID: "goal-turn-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "continue", GoalEpoch: 7, CreatedAt: g.now().UTC()}
	g.goalAfterJob(ctx, job, "turn answer")
	runner.mu.Lock()
	if len(runner.evals) != 1 {
		t.Fatalf("evals: %v", runner.evals)
	}
	eval := runner.evals[0]
	runner.mu.Unlock()
	if eval.Op != "evaluate" || eval.Engine != "hub" || eval.Goal != "ship it" ||
		!strings.Contains(eval.Response, "turn answer") {
		t.Fatalf("evaluate request: %+v", eval)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.TurnsUsed != 1 || goal.LastVerdict != "continue" {
		t.Fatalf("record: %+v", goal)
	}
	// The continue verdict queues the next turn under the same epoch.
	foundNext := false
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" && j.ID != "goal-turn-1" {
			if j.GoalEpoch == 7 && strings.Contains(j.Text, "next turn") {
				foundNext = true
			}
		}
	}
	if !foundNext {
		t.Fatal("continuation turn missing")
	}
}

// A native evaluate carries upstream's authoritative counters — the record
// mirrors them instead of incrementing a second, diverging count.
func TestGoalAfterJobNativeMirrorsTurnBudget(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "continue", Reason: "still working", ContinuationPrompt: "next turn", TurnsUsed: 4, MaxTurns: 5}}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "native", Status: "active", MaxTurns: 20, Epoch: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-1", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "continue", GoalEpoch: 7, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runner.mu.Lock()
		ready := len(runner.evals) > 0
		runner.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.TurnsUsed != 4 || goal.MaxTurns != 5 {
		t.Fatalf("record must mirror upstream budget: %+v", goal)
	}
	cancel()
	<-done
}

// A done verdict closes the goal and notifies the owner with the verdict and
// evidence excerpt — never a bare "done" claim (SPEC-0074 R3).
func TestGoalAfterJobDoneNotifiesWithEvidence(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "done", Reason: "tests pass", Evidence: "go test ok"}}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-done", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "continue", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
		if goal.Status == "done" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "done" || goal.LastVerdict != "done" {
		t.Fatalf("record: %+v", goal)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		ready := len(fake.sent) >= 2
		fake.mu.Unlock()
		if ready {
			break
		}
		g.deliverOne(ctx)
		time.Sleep(10 * time.Millisecond)
	}
	fake.mu.Lock()
	joined := strings.Join(fake.sent, "|")
	fake.mu.Unlock()
	if !strings.Contains(joined, "достигнута") || !strings.Contains(joined, "go test ok") {
		t.Fatalf("notification: %s", joined)
	}
	cancel()
	<-done
}

// A second goal lifetime in the same task restarts at epoch 1 after a clear —
// its done notification must not collide with the earlier goal's.
func TestGoalDoneNotificationsDoNotCollideAcrossLifetimes(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "done", Reason: "ok", Evidence: "e"}}
	g.api, g.runner = &fakeAPI{}, runner
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, jobID := range []string{"t1", "t2"} {
		if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "x", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
			t.Fatal(err)
		}
		job := Job{ID: "goal-turn-" + jobID, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 1, CreatedAt: g.now().UTC()}
		g.goalAfterJob(ctx, job, "done now")
	}
	count := 0
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		var d Delivery
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "outbox", "pending", e.Name())); err == nil && json.Unmarshal(data, &d) == nil && strings.HasPrefix(d.ID, "goal-") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("done notifications across goal lifetimes: %d", count)
	}
}

// A blocked verdict stops the loop and tells the owner why — no more turns
// are queued (SPEC-0074).
func TestGoalAfterJobBlockedStops(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "blocked", Reason: "needs credentials"}}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-blocked", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
		if goal.Status == "blocked" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "blocked" || goal.LastReason != "needs credentials" {
		t.Fatalf("record: %+v", goal)
	}
	time.Sleep(300 * time.Millisecond)
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			t.Fatalf("blocked goal must not queue turns: %+v", j)
		}
	}
	cancel()
	<-done
}

// The turn budget bounds the loop: the last allowed turn stops the goal even
// on a continue verdict (SPEC-0074).
func TestGoalAfterJobMaxTurnsStops(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", TurnsUsed: 2, MaxTurns: 3, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-max", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
		if goal.Status != "active" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "stopped" || goal.TurnsUsed != 3 {
		t.Fatalf("record: %+v", goal)
	}
	time.Sleep(200 * time.Millisecond)
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			t.Fatalf("stopped goal must not queue turns: %+v", j)
		}
	}
	cancel()
	<-done
}

// Three consecutive judge transport failures pause the goal rather than burn
// the budget on blind turns (SPEC-0074).
func TestGoalAfterJobTransportStreakPauses(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "continue", TransportFailure: true, Reason: "relay down"}}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 20, ConsecTransport: 2, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-transport", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
		if goal.Status == "paused" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "paused" || goal.ConsecTransport != 3 {
		t.Fatalf("record: %+v", goal)
	}
	time.Sleep(200 * time.Millisecond)
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" {
			t.Fatalf("paused goal must not queue turns: %+v", j)
		}
	}
	cancel()
	<-done
}

// A goal turn queued under a superseded epoch no-ops at claim: the model is
// never invoked for a goal that moved on (SPEC-0074).
func TestGoalStaleEpochNoOpsAtClaim(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "new goal", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{ID: "goal-turn-stale", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "x", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(g.spool.root, "done", "goal-turn-stale.json")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.Lock()
	if len(runner.runs) != 0 || len(runner.evals) != 0 {
		t.Fatalf("stale turn ran: runs=%v evals=%v", runner.runs, runner.evals)
	}
	runner.mu.Unlock()
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.TurnsUsed != 0 {
		t.Fatalf("stale turn counted: %+v", goal)
	}
	cancel()
	<-done
}

// A goal turn that hits a terminal run.failed gets the same single retry as a
// user message; when the retry also fails the record pauses instead of
// staying "active" with nothing queued — a zombie /goal status would still
// report live while the loop never runs again (SPEC-0074 R4: stops persist a
// reason).
func TestGoalTurnRunFailureRetriesThenPauses(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &alwaysFailRunner{}
	g.api, g.runner = fake, runner
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); g.worker(ctx) }()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "ship it", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.Enqueue(Job{Envelope: user.Envelope(11), ID: "goal-turn-fail", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", IdempotencyKey: "goal-turn-fail", ChatID: 11, TaskID: task.TaskID, Text: "kick", GoalEpoch: 1, CreatedAt: g.now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
		if goal.Status == "paused" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Status != "paused" || goal.LastReason == "" {
		t.Fatalf("failed goal turn left the record zombie-active: %+v", goal)
	}
	runner.mu.Lock()
	calls := runner.calls
	runner.mu.Unlock()
	if calls != 2 {
		t.Fatalf("runner calls=%d, want 2 (one automatic retry)", calls)
	}
	fake.mu.Lock()
	sent := append([]string{}, fake.sent...)
	fake.mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "/goal resume") {
		t.Fatalf("owner error delivery missing resume hint: %v", sent)
	}
}

// Hub-engine evaluations carry no continuation_prompt (that's the native
// sidecar's field): the gateway must still queue a turn whose prompt restates
// the goal text and last verdict reason (SPEC-0074 R3b) — an empty prompt is
// an instant "empty Hermes prompt" failure at run time.
func TestGoalAfterJobHubContinueRestatesGoal(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Verdict: "continue", Reason: "chapter 1 drafted"}}
	g.api, g.runner = fake, runner
	ctx := context.Background()
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "write the book", Engine: "hub", Status: "active", MaxTurns: 3, Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	job := Job{Envelope: user.Envelope(11), ID: "goal-turn-2", OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "goal", ChatID: 11, TaskID: task.TaskID, Text: "continue", GoalEpoch: 2, CreatedAt: g.now().UTC()}
	g.goalAfterJob(ctx, job, "drafted chapter 1")
	var next *Job
	entries, _ := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	for _, e := range entries {
		var j Job
		if data, err := os.ReadFile(filepath.Join(g.spool.root, "pending", e.Name())); err == nil && json.Unmarshal(data, &j) == nil && j.Trigger == "goal" && j.ID != job.ID {
			jj := j
			next = &jj
		}
	}
	if next == nil {
		t.Fatal("continuation turn missing")
	}
	if !strings.Contains(next.Text, "write the book") || !strings.Contains(next.Text, "chapter 1 drafted") {
		t.Fatalf("hub continuation must restate goal+verdict reason, got %q", next.Text)
	}
}

// /v1/goals is the ToolHub control surface: same record as the channel
// command, task resolved from the conversation id, caller pinned by the
// control bearer — never by a caller-supplied principal (SPEC-0074 R9).
func TestGoalsEndpointOps(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	runner := &goalLoopRunner{result: hubruntime.GoalResult{Engine: "hub", KickoffPrompt: "kick", MaxTurns: 20}}
	g.runner = runner
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	convID := task.ConversationID
	call := func(body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/goals", strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer control-token")
			r.Header.Set("X-Hub-Principal", "alice")
		}
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call(`{"op":"status","conversation_id":"`+convID+`"}`, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauth: %d", w.Code)
	}
	// set → record + kickoff job
	w := call(`{"op":"set","text":"ship it","conversation_id":"`+convID+`"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body.String())
	}
	goal, found, _ := g.spool.GoalForTask("alice", task.ConversationID)
	if !found || goal.Text != "ship it" || goal.Status != "active" {
		t.Fatalf("record: %+v found=%v", goal, found)
	}
	// status → bounded projection
	w = call(`{"op":"status","conversation_id":"`+convID+`"}`, true)
	var reply map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	goalMap, _ := reply["goal"].(map[string]any)
	if !found || goalMap["status"] != "active" || goalMap["engine"] != "hub" || goalMap["text"] != "ship it" {
		t.Fatalf("status reply: %v", reply)
	}
	// pause → record paused
	call(`{"op":"pause","conversation_id":"`+convID+`"}`, true)
	goal, _, _ = g.spool.GoalForTask("alice", task.ConversationID)
	if goal.Status != "paused" {
		t.Fatalf("paused: %+v", goal)
	}
	// resume → active + queued turn
	call(`{"op":"resume","conversation_id":"`+convID+`"}`, true)
	goal, _, _ = g.spool.GoalForTask("alice", task.ConversationID)
	if goal.Status != "active" {
		t.Fatalf("resumed: %+v", goal)
	}
	// clear → record gone
	call(`{"op":"clear","conversation_id":"`+convID+`"}`, true)
	if _, found, _ := g.spool.GoalForTask("alice", task.ConversationID); found {
		t.Fatal("record must be gone after clear")
	}
	// A well-formed foreign-chat conversation resolves to the caller's own
	// default task there — an empty goal, never the other chat's record.
	w = call(`{"op":"status","conversation_id":"telegram-999"}`, true)
	if w.Code != http.StatusOK {
		t.Fatalf("own default task in foreign chat must resolve: %d", w.Code)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	goalMap, _ = reply["goal"].(map[string]any)
	if goalMap["found"] != false || goalMap["text"] != "" {
		t.Fatalf("cross-chat goal must not leak: %v", reply)
	}
	// Malformed conversation ids resolve to nothing.
	w = call(`{"op":"status","conversation_id":"bogus"}`, true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("malformed conversation must not resolve: %d %s", w.Code, w.Body.String())
	}
}

// A new owner message preempts queued goal turns: it bumps the record epoch
// so the pending continuation no-ops at claim instead of interrupting the
// user's turn (SPEC-0074).
func TestGoalUserMessagePreemptsContinuation(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	runner := &goalLoopRunner{}
	g.api, g.runner = fake, runner
	user := g.user("alice")
	task, err := g.spool.ResolveTask(user, 11, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.SaveGoal(Goal{TaskID: task.TaskID, PrincipalID: user.ID, ConversationID: task.ConversationID, Text: "old goal", Engine: "hub", Status: "active", MaxTurns: 20, Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if err := g.handleUpdate(context.Background(), taskUpdate(1, 1, "actually check the deploy", 11, 0)); err != nil {
		t.Fatal(err)
	}
	goal, _, _ := g.spool.GoalForTask(user.ID, task.ConversationID)
	if goal.Epoch != 2 {
		t.Fatalf("epoch not bumped: %+v", goal)
	}
}
