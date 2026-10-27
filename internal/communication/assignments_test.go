package communication

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
)

// TestAssignmentAPIRoundTrip (SPEC-0080 R2.1/R2.2): create→apply→get→pause→
// resume→cancel over REST; every route is owner-scoped (foreign ⇒ 404) and
// cancelled is terminal.
func TestAssignmentAPIRoundTrip(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"title":"watch prices","intent":"watch prices for X","apply":true,"plan":{"sources":[{"name":"page","schedule":{"schedule_id":"src-page","channel":"telegram_bot","chat_id":11,"timezone":"UTC","expression":"every:15m","job_kind":"watcher","input":"watch page","probe":{"kind":"http","url":"https://example.com/p"}}}],"specialist":{"instructions":"report changes only","reply":"notify"}}}`
	w := destinationRequest(g, http.MethodPost, "/v1/assignments", body)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	var created commodel.Assignment
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.AssignmentID == "" || created.State != "active" {
		t.Fatalf("assignment not active after apply: %+v", created)
	}
	id := created.AssignmentID

	// List shows the assignment to its owner.
	w = destinationRequest(g, http.MethodGet, "/v1/assignments", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("list missing assignment: %d %s", w.Code, w.Body.String())
	}
	// Status card reports applied artifacts.
	w = destinationRequest(g, http.MethodGet, "/v1/assignments/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("card failed: %d %s", w.Code, w.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card["state"] != "active" || card["assignment_id"] != id {
		t.Fatalf("bad card: %v", card)
	}
	// Pause → resume → cancel lifecycle over REST — each verb must reach the
	// durable state, not just return 200.
	for _, step := range []struct{ action, want string }{{"pause", "paused"}, {"resume", "active"}, {"cancel", "cancelled"}} {
		w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+id+"/"+step.action, `{}`)
		if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
			t.Fatalf("%s failed: %d %s", step.action, w.Code, w.Body.String())
		}
		item, err := g.spool.rt.LoadAssignment(id, g.users[11].Envelope(11))
		if err != nil || item.State != step.want {
			t.Fatalf("%s did not reach state %q: %v %+v", step.action, step.want, err, item)
		}
	}
	// Cancelled is terminal: re-apply refuses.
	w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+id+"/apply", `{}`)
	if w.Code == http.StatusOK {
		t.Fatal("cancelled assignment must not re-apply")
	}
	// Foreign id ⇒ 404-class everywhere.
	for _, path := range []string{"/v1/assignments/nope", "/v1/assignments/nope/pause"} {
		w = destinationRequest(g, http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("foreign id %s must 404, got %d", path, w.Code)
		}
	}
}

// TestAssignmentPlanDryRun (SPEC-0080 R2.1): the dry-run endpoint validates
// every source draft and returns per-source verdicts without persisting any
// assignment or schedule.
func TestAssignmentPlanDryRun(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"sources":[{"name":"page","schedule":{"channel":"telegram_bot","chat_id":11,"timezone":"UTC","expression":"every:15m","job_kind":"watcher","input":"watch page","probe":{"kind":"http","url":"https://example.com/p"}}},{"name":"bad","schedule":{"channel":"telegram_bot","chat_id":11,"timezone":"UTC","expression":"every:15m","job_kind":"watcher","input":"watch bad","probe":{"kind":"http","url":"not-a-url"}}}]}`
	w := destinationRequest(g, http.MethodPost, "/v1/assignments/plan", body)
	if w.Code != http.StatusOK {
		t.Fatalf("dry-run failed: %d %s", w.Code, w.Body.String())
	}
	var verdicts map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &verdicts); err != nil {
		t.Fatal(err)
	}
	if verdicts["page"] != "ok" || verdicts["bad"] == "ok" || verdicts["bad"] == "" {
		t.Fatalf("per-source verdicts wrong: %v", verdicts)
	}
	if items, _ := g.spool.rt.ListAssignments(g.users[11].Envelope(11)); len(items) != 0 {
		t.Fatal("dry-run must not persist")
	}
}

// TestAssignmentPlanUpdateNarrowsGrants (SPEC-0080 R2.4): POST .../plan is
// CAS-guarded; grant widening without owner authorization is refused.
func TestAssignmentPlanUpdateNarrowsGrants(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/assignments", `{"title":"t","intent":"watch x","grants":{"max_wakes_per_day":10}}`)
	var created commodel.Assignment
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.AssignmentID == "" {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	id := created.AssignmentID
	// Widening without authorize ⇒ refused.
	w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+id+"/plan", `{"plan":{"revision":1,"sources":[]},"grants":{"max_wakes_per_day":50},"expected_revision":`+itoa(created.Revision)+`}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("grant widening must 403, got %d %s", w.Code, w.Body.String())
	}
	// Narrowing ⇒ ok; stale revision ⇒ conflict.
	w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+id+"/plan", `{"plan":{"revision":1,"sources":[]},"grants":{"max_wakes_per_day":5},"expected_revision":`+itoa(created.Revision)+`}`)
	if w.Code != http.StatusOK {
		t.Fatalf("narrowing refused: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+id+"/plan", `{"plan":{"revision":1,"sources":[]},"grants":{"max_wakes_per_day":4},"expected_revision":`+itoa(created.Revision)+`}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision must 409, got %d %s", w.Code, w.Body.String())
	}
}

func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

// TestAssignmentCompleteVerb (SPEC-0080 R2.1): complete retires the
// assignment's sources and lands in the terminal "completed" state.
func TestAssignmentCompleteVerb(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"title":"done","intent":"watch y","apply":true,"plan":{"sources":[{"name":"page","schedule":{"channel":"telegram_bot","chat_id":11,"timezone":"UTC","expression":"every:15m","job_kind":"watcher","input":"w","probe":{"kind":"http","url":"https://example.com/p"}}}]}}`
	w := destinationRequest(g, http.MethodPost, "/v1/assignments", body)
	var created commodel.Assignment
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.AssignmentID == "" {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/assignments/"+created.AssignmentID+"/complete", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("complete failed: %d %s", w.Code, w.Body.String())
	}
	item, err := g.spool.rt.LoadAssignment(created.AssignmentID, g.users[11].Envelope(11))
	if err != nil || item.State != "completed" {
		t.Fatalf("not completed: %v %+v", err, item)
	}
}

// TestAssignmentCommands (SPEC-0080 R2.4/R2.5): /assignments lists the
// owner's standing assignments; /assignment <id> status|pause|resume|
// cancel|complete|authorize steers them — owner-envelope only, authorize is
// chat-exclusive.
func TestAssignmentCommands(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := g.users[11]
	w := destinationRequest(g, http.MethodPost, "/v1/assignments", `{"title":"prices","intent":"watch prices","apply":true,"plan":{"sources":[{"name":"page","schedule":{"channel":"telegram_bot","chat_id":11,"timezone":"UTC","expression":"every:15m","job_kind":"watcher","input":"w","probe":{"kind":"http","url":"https://example.com/p"}}}],"specialist":{"instructions":"report changes"}}}`)
	var created commodel.Assignment
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.AssignmentID == "" {
		t.Fatalf("create failed: %d %s", w.Code, w.Body.String())
	}
	id := created.AssignmentID

	out := g.assignmentsCommand(user, 11)
	if !strings.Contains(out, id) || !strings.Contains(out, "active") {
		t.Fatalf("list command missing assignment: %q", out)
	}
	out = g.assignmentCommand(user, 11, "/assignment "+id+" status")
	if !strings.Contains(out, "active") {
		t.Fatalf("status command wrong: %q", out)
	}
	out = g.assignmentCommand(user, 11, "/assignment "+id+" pause")
	item, _ := g.spool.rt.LoadAssignment(id, user.Envelope(11))
	if !strings.Contains(out, "pause") && item.State != "paused" {
		t.Fatalf("pause command failed: %q state=%s", out, item.State)
	}
	if item.State != "paused" {
		t.Fatalf("pause command did not pause: %s", item.State)
	}
	// authorize stamps the plan revision — the only grant-widening verb.
	out = g.assignmentCommand(user, 11, "/assignment "+id+" authorize")
	item, _ = g.spool.rt.LoadAssignment(id, user.Envelope(11))
	if !strings.Contains(out, "authorize") || item.GrantsAuthorizedRev != item.Plan.Revision {
		t.Fatalf("authorize did not stamp plan revision: %+v (out=%q)", item, out)
	}
	// Foreign sender gets a refusal, not the record.
	out = g.assignmentCommand(user, 99, "/assignment "+id+" status")
	if strings.Contains(out, "active") {
		t.Fatalf("foreign sender saw the assignment: %q", out)
	}
}

// TestAssignmentCommandDispatch (SPEC-0080 R2.4/R2.5): the chat dispatcher
// routes /assignments and /assignment to the owner-scoped command handlers
// and the help inventory advertises both.
func TestAssignmentCommandDispatch(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	g.api, g.runner = fake, &fakeRunner{}
	ctx := context.Background()
	if err := g.handleUpdate(ctx, taskUpdate(1, 1, "/assignments", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 1 || !strings.HasPrefix(fake.sent[0], "Постоянные поручения") {
		t.Fatalf("/assignments did not route: %v", fake.sent)
	}
	if err := g.handleUpdate(ctx, taskUpdate(2, 1, "/assignment missing-id status", 11, 0)); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(ctx)
	if len(fake.sent) != 2 || !strings.HasPrefix(fake.sent[1], "Поручение недоступно") {
		t.Fatalf("/assignment did not route: %v", fake.sent)
	}
	help := commandHelp()
	if !strings.Contains(help, "/assignments — ") || !strings.Contains(help, "/assignment — ") {
		t.Fatalf("help inventory missing assignment commands: %q", help)
	}
}
