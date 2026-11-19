package communication

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func TestSessionControlReportsNextTurnBoundary(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := newTestGateway(t, c)
	if err != nil {
		t.Fatal(err)
	}
	created := destinationRequest(g, http.MethodPost, "/v1/sessions", `{"action":"create","title":"next-turn"}`)
	var response map[string]any
	if created.Code != http.StatusOK || json.Unmarshal(created.Body.Bytes(), &response) != nil || response["effective_from"] != "next_turn" {
		t.Fatalf("create boundary: %d %s", created.Code, created.Body.String())
	}
	session := response["session"].(map[string]any)
	id := session["id"].(string)
	switched := destinationRequest(g, http.MethodPost, "/v1/sessions", `{"action":"switch","id":"`+id+`"}`)
	response = nil
	if switched.Code != http.StatusOK || json.Unmarshal(switched.Body.Bytes(), &response) != nil || response["effective_from"] != "next_turn" || response["switched_to"] != id {
		t.Fatalf("switch boundary: %d %s", switched.Code, switched.Body.String())
	}
}

func sessionGateway(t *testing.T, sessionRead map[string]any) *Gateway {
	t.Helper()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	c.RuntimeAuth = "runtime-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/session-read" || r.Header.Get("Authorization") != "Bearer runtime-token" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sessionRead)
	}))
	t.Cleanup(upstream.Close)
	c.RuntimeURL = upstream.URL
	g, err := newTestGateway(t, c)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// seedSessionJob lands a completed mapping whose SessionID binds the task's
// conversation — the "read" branch reads its transcript upstream.
func seedSessionJob(t *testing.T, g *Gateway, conversation, sessionID, result string) {
	t.Helper()
	job := Job{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), ID: "job-" + sessionID, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", IdempotencyKey: "idem-" + sessionID, Text: "q", CreatedAt: time.Now().UTC()}
	job.ConversationID = conversation
	if _, err := g.spool.Enqueue(job); err != nil {
		t.Fatal(err)
	}
	if _, err := g.spool.ClaimJob(); err != nil {
		t.Fatal(err)
	}
	if err := g.spool.RecordOutcome(job.ID, RunOutcome{JobID: job.ID, SessionID: sessionID, RunID: "run-" + sessionID, RuntimeGeneration: "gen-1", Status: "completed", LastEvent: "run.completed", Text: result}); err != nil {
		t.Fatal(err)
	}
}

func sessionPost(t *testing.T, g *Gateway, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	w := destinationRequest(g, http.MethodPost, "/v1/sessions", body)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestSessionListSearchAndPagination(t *testing.T) {
	g := sessionGateway(t, nil)
	user := g.user("alice")
	seeded, _, err := g.spool.CreateTask(user, 11, 0, "alpha-notes", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.spool.CreateTask(user, 11, 0, "beta-thread", false); err != nil {
		t.Fatal(err)
	}
	seedSessionJob(t, g, seeded.ConversationID, "sess-alpha", "the retained needle reply")

	w, out := sessionPost(t, g, `{"action":"list","limit":1}`)
	if w.Code != http.StatusOK || len(out["items"].([]any)) != 1 || out["next_cursor"] == "" {
		t.Fatalf("list page one: %d %s", w.Code, w.Body.String())
	}
	w, out = sessionPost(t, g, `{"action":"list","cursor":"`+out["next_cursor"].(string)+`"}`)
	if w.Code != http.StatusOK || len(out["items"].([]any)) == 0 {
		t.Fatalf("list page two: %d %s", w.Code, w.Body.String())
	}
	// Search falls through to retained model replies when the title misses.
	w, out = sessionPost(t, g, `{"action":"search","query":"needle"}`)
	items, _ := out["items"].([]any)
	if w.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("search by reply text: %d %s", w.Code, w.Body.String())
	}
	// Query with no hit anywhere returns an empty page.
	w, out = sessionPost(t, g, `{"action":"search","query":"absent-marker"}`)
	if w.Code != http.StatusOK || len(out["items"].([]any)) != 0 {
		t.Fatalf("search miss: %d %s", w.Code, w.Body.String())
	}
	w, _ = sessionPost(t, g, `{"action":"search","query":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty search accepted: %d", w.Code)
	}
	w, _ = sessionPost(t, g, `{"action":"list","cursor":"bogus"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad cursor accepted: %d", w.Code)
	}
}

func TestSessionReadTranscriptAndNotStarted(t *testing.T) {
	g := sessionGateway(t, map[string]any{"declared_session_id": "sess-1", "messages": []any{map[string]any{"role": "assistant", "text": "hi"}}})
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "read-me", true)
	if err != nil {
		t.Fatal(err)
	}
	// No mapping yet → not_started without an upstream call.
	w, out := sessionPost(t, g, `{"action":"read","id":"`+task.TaskID+`"}`)
	if w.Code != http.StatusOK || out["status"] != "not_started" {
		t.Fatalf("read not_started: %d %s", w.Code, w.Body.String())
	}
	seedSessionJob(t, g, task.ConversationID, "sess-1", "answer")
	w, out = sessionPost(t, g, `{"action":"read","id":"`+task.TaskID+`","limit":60}`)
	if w.Code != http.StatusOK {
		t.Fatalf("read transcript: %d %s", w.Code, w.Body.String())
	}
	if _, ok := out["transcript"]; !ok {
		t.Fatalf("transcript missing: %s", w.Body.String())
	}
}

func TestSessionReadUpstreamRefusal(t *testing.T) {
	g := sessionGateway(t, nil)
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "read-refused", true)
	if err != nil {
		t.Fatal(err)
	}
	seedSessionJob(t, g, task.ConversationID, "sess-9", "answer")
	// Upstream answers a mismatched declared_session_id → unavailable.
	w, _ := sessionPost(t, g, `{"action":"read","id":"`+task.TaskID+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("mismatched upstream accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestSessionEditArchiveRestoreAndRemove(t *testing.T) {
	g := sessionGateway(t, nil)
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "edit-me", true)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := g.spool.CreateTask(user, 11, 0, "edit-target", false)
	if err != nil {
		t.Fatal(err)
	}
	w, out := sessionPost(t, g, `{"action":"edit","id":"`+other.TaskID+`","edit_action":"archive"}`)
	if w.Code != http.StatusOK || out["status"] != "archived" {
		t.Fatalf("archive: %d %s", w.Code, w.Body.String())
	}
	// Switch refuses an archived task.
	w, _ = sessionPost(t, g, `{"action":"switch","id":"`+other.TaskID+`"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("switch to archived: %d", w.Code)
	}
	w, out = sessionPost(t, g, `{"action":"edit","id":"`+other.TaskID+`","edit_action":"restore"}`)
	if w.Code != http.StatusOK || out["status"] != "active" {
		t.Fatalf("restore: %d %s", w.Code, w.Body.String())
	}
	// Second archive of an already-live task is the conflicting combo guard:
	// archive on archived refuses.
	if _, err := g.spool.ArchiveTask(user, 11, other.TaskID); err != nil {
		t.Fatal(err)
	}
	w, _ = sessionPost(t, g, `{"action":"edit","id":"`+other.TaskID+`","edit_action":"archive"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("double archive accepted: %d", w.Code)
	}
	// Default session is protected from edit/remove.
	w, _ = sessionPost(t, g, `{"action":"edit","id":"`+defaultTaskID+`","edit_action":"archive"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("default session edit accepted: %d", w.Code)
	}
	_ = task
	w, _ = sessionPost(t, g, `{"action":"remove","id":"`+defaultTaskID+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("default session remove accepted: %d", w.Code)
	}
	w, out = sessionPost(t, g, `{"action":"remove","id":"`+other.TaskID+`"}`)
	if w.Code != http.StatusOK || out["removed"] != true || out["transcript_retained_for_audit"] != true {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	// Removed task is gone.
	w, _ = sessionPost(t, g, `{"action":"read","id":"`+other.TaskID+`"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("removed task readable: %d", w.Code)
	}
}

func TestSessionControlRefusals(t *testing.T) {
	g := sessionGateway(t, nil)
	// Unauthenticated.
	r := httptest.NewRequest(http.MethodPost, "/v1/sessions", nil)
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated accepted: %d", w.Code)
	}
	// Wrong verb.
	w, _ = sessionPost(t, g, `{"action":"explode"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown action accepted: %d", w.Code)
	}
	// id-less action needs the id.
	w, _ = sessionPost(t, g, `{"action":"read"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("id-less read accepted: %d", w.Code)
	}
	// Create needs a title.
	w, _ = sessionPost(t, g, `{"action":"create"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("title-less create accepted: %d", w.Code)
	}
	// Edit needs a valid edit_action.
	user := g.user("alice")
	task, _, err := g.spool.CreateTask(user, 11, 0, "guard", true)
	if err != nil {
		t.Fatal(err)
	}
	w, _ = sessionPost(t, g, `{"action":"edit","id":"`+task.TaskID+`","edit_action":"nuke"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad edit_action accepted: %d", w.Code)
	}
	// Unknown task id.
	w, _ = sessionPost(t, g, `{"action":"read","id":"task-missing"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing task read: %d", w.Code)
	}
}
