package communication

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func destinationFixture(t *testing.T) (*Gateway, identity.Envelope, Task, Task) {
	t.Helper()
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	user := c.Users[0]
	env := user.Envelope(11)
	a, _, err := g.spool.CreateTask(user, 11, 5, "alpha", false)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := g.spool.CreateTask(user, 11, 6, "beta", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id   string
		task Task
	}{{"origin-a", a}, {"origin-b", b}} {
		job := Job{ID: row.id, Envelope: env, OrganizationID: c.OrganizationID, UserID: user.ID, ActorID: user.ID, ScopeID: "user:" + user.ID, Channel: "telegram_bot", ChatID: 11, TaskID: row.task.TaskID, TopicID: row.task.TopicID, Trigger: "message", Text: "fixture", IdempotencyKey: row.id}
		job.ConversationID = row.task.ConversationID
		if err := atomicJSON(g.spool.mappingPath(job.ID), mappingFromJob(job, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	return g, env, a, b
}

func TestRoutineDestinationDiscoveryIsScopedAndPaged(t *testing.T) {
	g, _, a, b := destinationFixture(t)
	other, _, err := g.spool.CreateTask(User{ID: "bob", Enabled: true, TelegramIDs: []int64{22}}, 22, 7, "private", false)
	if err != nil {
		t.Fatal(err)
	}
	_ = other
	archived, _, err := g.spool.CreateTask(g.user("alice"), 11, 8, "archived", false)
	if err != nil {
		t.Fatal(err)
	}
	archived.Archived = true
	if err := atomicJSON(g.spool.taskPath("alice", archived.TaskID), archived); err != nil {
		t.Fatal(err)
	}
	foreign, _, err := g.spool.CreateTask(g.user("alice"), 11, 9, "foreign-context", false)
	if err != nil {
		t.Fatal(err)
	}
	foreign.ContextID = "foreign"
	if err := atomicJSON(g.spool.taskPath("alice", foreign.TaskID), foreign); err != nil {
		t.Fatal(err)
	}
	want := []string{"default", a.TaskID, b.TaskID}
	sort.Strings(want)
	cursor := ""
	for _, id := range want {
		w := destinationRequest(g, http.MethodGet, "/v1/routines?view=destinations&limit=1&after="+cursor, "")
		var page struct {
			Items      []Task `json:"items"`
			NextCursor string `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || len(page.Items) != 1 || page.Items[0].TaskID != id {
			t.Fatalf("destination page=%s want=%s", w.Body.String(), id)
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("foreign or archived destination leaked", cursor)
	}
	w := destinationRequest(g, http.MethodGet, "/v1/routines?view=destinations&query=ALPHA&limit=10", "")
	var page struct {
		Items []Task `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TaskID != a.TaskID || page.Items[0].Name != "alpha" || page.Items[0].TopicID != 5 {
		t.Fatalf("destination search=%s", w.Body.String())
	}
}

func TestRoutineDestinationRejectsUnownedOrStaleBindings(t *testing.T) {
	for _, name := range []string{"missing-origin", "foreign-origin", "context-origin", "policy-origin", "topic-origin", "missing-task", "archived-task", "context-task", "chat-task", "unbound-topic"} {
		t.Run(name, func(t *testing.T) {
			g, env, a, b := destinationFixture(t)
			origin, target := "", a.TaskID
			if strings.HasSuffix(name, "origin") {
				origin, target = "origin-a", ""
				if name == "missing-origin" {
					origin = "missing"
				} else {
					mapping, err := g.spool.loadMappingLocked(origin)
					if err != nil {
						t.Fatal(err)
					}
					switch name {
					case "foreign-origin":
						mapping.PrincipalID = "bob"
					case "context-origin":
						mapping.ContextID = "foreign"
					case "policy-origin":
						mapping.PolicyVersion = "revoked"
					case "topic-origin":
						mapping.TopicID = 99
					}
					if err := atomicJSON(g.spool.mappingPath(origin), mapping); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				switch name {
				case "missing-task":
					target = "missing"
				case "archived-task":
					a.Archived = true
				case "context-task":
					a.ContextID = "foreign"
				case "chat-task":
					a.ChatID = 22
				case "unbound-topic":
					if err := atomicJSON(g.spool.topicTaskPath("alice", 11, 5), struct {
						TaskID string `json:"task_id"`
					}{b.TaskID}); err != nil {
						t.Fatal(err)
					}
				}
				if err := atomicJSON(g.spool.taskPath("alice", a.TaskID), a); err != nil {
					t.Fatal(err)
				}
			}
			body := fmt.Sprintf(`{"schedule_id":"denied","origin_job_id":%q,"destination_task_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, origin, target)
			w := destinationRequest(g, http.MethodPost, "/v1/routines", body)
			if w.Code != 400 && w.Code != 403 {
				t.Errorf("unsafe route accepted: %d %s", w.Code, w.Body.String())
			}
			if _, err := g.spool.loadScheduleLocked("denied"); !os.IsNotExist(err) {
				t.Errorf("denied route persisted: %v", err)
			}
			_ = env
		})
	}
}

func destinationRequest(g *Gateway, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer control-token")
	r.Header.Set("X-Hub-Principal", "alice")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, r)
	return w
}

func TestRoutineOriginPinsExactTaskInsteadOfCurrentPointer(t *testing.T) {
	g, _, a, b := destinationFixture(t)
	if _, err := g.spool.UseTask(g.user("alice"), 11, b.TaskID); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		origin, id string
		task       Task
	}{{"origin-a", "source-a", a}, {"origin-b", "source-b", b}} {
		body := fmt.Sprintf(`{"schedule_id":%q,"origin_job_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, row.id, row.origin)
		w := destinationRequest(g, http.MethodPost, "/v1/routines", body)
		var got Schedule
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.TaskID != row.task.TaskID || got.TopicID != row.task.TopicID || got.ChatID != 11 {
			t.Fatalf("origin=%s got=%s want task=%s topic=%d", row.origin, w.Body.String(), row.task.TaskID, row.task.TopicID)
		}
	}
}

func TestRoutineDestinationCurrentAndDefaultAreExplicit(t *testing.T) {
	g, env, a, _ := destinationFixture(t)
	root := Job{ID: "origin-root", Envelope: env, OrganizationID: g.config.OrganizationID, UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Trigger: "message", Text: "fixture", IdempotencyKey: "origin-root"}
	if err := atomicJSON(g.spool.mappingPath(root.ID), mappingFromJob(root, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, origin, target, want string
		topic                    int64
		status                   int
	}{
		{"explicit-current", "origin-a", "current", a.TaskID, 5, 200},
		{"missing-current", "", "current", "", 0, 400},
		{"explicit-root", "", "default", "default", 0, 200},
		{"origin-root-route", "origin-root", "", "default", 0, 200},
	} {
		body := fmt.Sprintf(`{"schedule_id":%q,"origin_job_id":%q,"destination_task_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, row.id, row.origin, row.target)
		w := destinationRequest(g, http.MethodPost, "/v1/routines", body)
		if w.Code != row.status {
			t.Errorf("%s status=%d body=%s", row.id, w.Code, w.Body.String())
			continue
		}
		if row.status == 200 {
			var got Schedule
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.TaskID != row.want || got.TopicID != row.topic {
				t.Errorf("%s wrong destination task=%s topic=%d", row.id, got.TaskID, got.TopicID)
			}
		}
	}
}

func TestRoutineCardShowsSavedDestinationWithoutFallback(t *testing.T) {
	g, _, a, _ := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"card-route","destination_task_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, state := range []string{"active", "archived", "unavailable"} {
		if state == "archived" {
			a.Archived = true
			if err := atomicJSON(g.spool.taskPath("alice", a.TaskID), a); err != nil {
				t.Fatal(err)
			}
		}
		if state == "unavailable" {
			if err := os.Remove(g.spool.taskPath("alice", a.TaskID)); err != nil {
				t.Fatal(err)
			}
		}
		w = destinationRequest(g, http.MethodGet, "/v1/routines/card-route", "")
		var card struct {
			Destination RoutineDestination `json:"destination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || card.Destination.TaskID != a.TaskID || card.Destination.ChatID != 11 || card.Destination.TopicID != 5 || card.Destination.State != state {
			t.Errorf("destination %s: %s", state, w.Body.String())
		}
		if state != "unavailable" && card.Destination.Name != "alpha" {
			t.Error("missing destination name", card.Destination)
		}
	}
	for _, target := range []string{"default", ""} {
		w = destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"root-%s","destination_task_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, target, target))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		w = destinationRequest(g, http.MethodGet, "/v1/routines/root-"+target, "")
		var card struct {
			Destination RoutineDestination `json:"destination"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
			t.Fatal(err)
		}
		if card.Destination.TaskID != "default" || card.Destination.Name != "default" || card.Destination.State != "active" || card.Destination.TopicID != 0 {
			t.Errorf("default destination=%s", w.Body.String())
		}
	}
}

func TestRoutineCardDoesNotReportBrokenBindingsHealthy(t *testing.T) {
	for _, field := range []string{"context", "chat", "topic", "pointer"} {
		t.Run(field, func(t *testing.T) {
			g, _, a, _ := destinationFixture(t)
			w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"broken","destination_task_id":%q,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`, a.TaskID))
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			switch field {
			case "context":
				a.ContextID = "foreign"
			case "chat":
				a.ChatID = 22
			case "topic":
				a.TopicID = 99
			case "pointer":
				if err := atomicJSON(g.spool.topicTaskPath("alice", 11, 5), map[string]string{"task_id": "another-task"}); err != nil {
					t.Fatal(err)
				}
			}
			if err := atomicJSON(g.spool.taskPath("alice", a.TaskID), a); err != nil {
				t.Fatal(err)
			}
			w = destinationRequest(g, http.MethodGet, "/v1/routines/broken", "")
			var card struct {
				Destination RoutineDestination `json:"destination"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
				t.Fatal(err)
			}
			if card.Destination.State != "unavailable" || card.Destination.Name != "" || card.Destination.ChatID != 11 || card.Destination.TopicID != 5 {
				t.Errorf("broken route looks healthy or substituted: %s", w.Body.String())
			}
			if field == "pointer" {
				g.tickProactive(time.Date(2099, 1, 1, 10, 0, 0, 0, time.UTC))
				// The routine blocks on the stale binding and the owner gets
				// one notice — at their home, never through the stale topic.
				d, err := g.spool.ClaimDelivery()
				if err != nil || d == nil || d.TopicID != 0 || d.TaskID != "" {
					t.Errorf("stale topic pointer admitted delivery: %+v %v", d, err)
				}
			}
		})
	}
	g, _, _, _ := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"legacy-topic","topic_id":77,"timezone":"UTC","expression":"once:2099-01-01T10:00:00Z","job_kind":"reminder","input":"fixture"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/legacy-topic", "")
	var card struct {
		Destination RoutineDestination `json:"destination"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Destination.State != "unavailable" || card.Destination.TopicID != 77 {
		t.Errorf("unbound legacy topic=%s", w.Body.String())
	}
}

func TestRoutineDestinationEditPreservesScheduleAndFencesRevision(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"move","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if err := g.spool.PauseSchedule("move", env, 1); err != nil {
		t.Fatal(err)
	}
	until := time.Date(2099, 1, 2, 12, 0, 0, 0, time.UTC)
	if err := g.spool.SnoozeSchedule("move", env, until, 2); err != nil {
		t.Fatal(err)
	}
	before, err := g.spool.GetRoutine("move", env)
	if err != nil {
		t.Fatal(err)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/move", fmt.Sprintf(`{"expected_revision":3,"destination_task_id":%q}`, b.TaskID))
	if w.Code != 200 {
		t.Fatalf("move failed: %d %s", w.Code, w.Body.String())
	}
	card, err := g.spool.GetRoutine("move", env)
	if err != nil {
		t.Fatal(err)
	}
	if card.Schedule.TaskID != b.TaskID || card.Schedule.TopicID != 6 || card.Schedule.Revision != 4 || !card.Schedule.Paused || card.Schedule.Expression != before.Schedule.Expression || !card.Schedule.NextDue.Equal(before.Schedule.NextDue) || !card.Schedule.SnoozedUntil.Equal(before.Schedule.SnoozedUntil) {
		t.Fatalf("move changed timing/state: %+v", card.Schedule)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/move", fmt.Sprintf(`{"expected_revision":3,"destination_task_id":%q}`, a.TaskID))
	if w.Code != 409 {
		t.Fatalf("stale route edit: %d %s", w.Code, w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/move", `{"expected_revision":4,"destination_task_id":"current","origin_job_id":"origin-a"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err = g.spool.GetRoutine("move", env)
	if err != nil || card.Schedule.TaskID != a.TaskID || card.Schedule.Revision != 5 || card.Schedule.TopicID != 5 {
		t.Fatalf("current route not restored: %+v %v", card, err)
	}
}

func TestRoutineDestinationEditRejectsInvalidTargetWithoutMutation(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"reject-move","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	b.Archived = true
	if err := atomicJSON(g.spool.taskPath("alice", b.TaskID), b); err != nil {
		t.Fatal(err)
	}
	for _, patch := range []string{`"destination_task_id":null`, `"destination_task_id":""`, `"destination_task_id":{}`, `"destination_task_id":"missing"`, `"destination_task_id":"current"`, `"origin_job_id":"origin-a"`, fmt.Sprintf(`"destination_task_id":%q`, b.TaskID)} {
		w = destinationRequest(g, http.MethodPost, "/v1/routines/reject-move", `{"expected_revision":1,`+patch+`}`)
		if w.Code != 400 {
			t.Errorf("invalid target accepted: %d %s", w.Code, w.Body.String())
		}
		card, err := g.spool.GetRoutine("reject-move", env)
		if err != nil || card.Schedule.Revision != 1 || card.Schedule.TaskID != a.TaskID {
			t.Fatalf("invalid target mutated: %+v %v", card, err)
		}
	}
}

func TestRoutineDestinationEditRequiresRevisionEvenOnPrivateHTTP(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"fenced-route","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	for _, revision := range []string{"", `"expected_revision":0,`} {
		w = destinationRequest(g, http.MethodPost, "/v1/routines/fenced-route", `{`+revision+fmt.Sprintf(`"destination_task_id":%q}`, b.TaskID))
		if w.Code != 400 {
			t.Errorf("unfenced destination accepted: %d %s", w.Code, w.Body.String())
		}
		card, err := g.spool.GetRoutine("fenced-route", env)
		if err != nil || card.Schedule.Revision != 1 || card.Schedule.TaskID != a.TaskID {
			t.Errorf("unfenced route mutated: %+v %v", card, err)
		}
	}
}

func TestRoutineDestinationMoveRetainsPriorAudienceHistory(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	user := g.user("alice")
	user.TelegramIDs = append(user.TelegramIDs, 12)
	g.users[11] = user
	g.users[12] = user
	g.config.Users[0] = user
	b.ChatID = 12
	b.TopicID = 0
	if err := atomicJSON(g.spool.taskPath("alice", b.TaskID), b); err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"history-move","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("history-move", env)
	if err != nil {
		t.Fatal(err)
	}
	receipt := routineHistoryReceipt{ID: "history-fixture", ScheduleID: "history-move", Envelope: card.Schedule.Envelope, Run: RoutineRun{ID: "history-fixture", State: "completed", DeliveryState: "delivered"}}
	path := filepath.Join(g.spool.root, "occurrences", "receipts", "history-fixture.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(path, receipt); err != nil {
		t.Fatal(err)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/history-move", fmt.Sprintf(`{"expected_revision":1,"destination_task_id":%q}`, b.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	history, err := g.spool.RoutineHistory("history-move", env)
	if err != nil || len(history) != 1 || history[0].DeliveryState != "delivered" {
		t.Fatalf("history lost after cross-chat move: %+v %v", history, err)
	}
	receipt.Envelope.PrincipalID = "mallory"
	if err := atomicJSON(path, receipt); err != nil {
		t.Fatal(err)
	}
	history, err = g.spool.RoutineHistory("history-move", env)
	if err != nil || len(history) != 0 {
		t.Fatalf("foreign history visible: %+v %v", history, err)
	}
}

func TestRoutineDestinationPriorAudienceIsServerOwned(t *testing.T) {
	g, env, _, _ := destinationFixture(t)
	forged := env
	forged.DeliveryTargetID = "forged"
	draft := Schedule{PriorEnvelopes: []identity.Envelope{forged}}
	clean := normalizeScheduleDraft(draft)
	if len(clean.PriorEnvelopes) != 0 {
		t.Error("creation accepts injected prior audience")
	}
	clean.PriorEnvelopes = nil
	if scheduleIntent(draft) != scheduleIntent(clean) {
		t.Error("server history changes creation intent")
	}
	w := destinationRequest(g, http.MethodPost, "/v1/routines", `{"schedule_id":"server-history","destination_task_id":"default","timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture","prior_envelopes":[{"principal_id":"mallory"}]}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("server-history", env)
	if err != nil || len(card.Schedule.PriorEnvelopes) != 0 {
		t.Errorf("injected prior history persisted: %+v %v", card.Schedule.PriorEnvelopes, err)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/server-history", `{"expected_revision":1,"prior_envelopes":[]}`)
	if w.Code != 400 {
		t.Errorf("prior history editable: %d", w.Code)
	}
}

func TestRoutineDestinationCardRetainsLastOccurrenceAfterOwnedChatMove(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	user := g.user("alice")
	user.TelegramIDs = append(user.TelegramIDs, 12)
	g.users[11], g.users[12], g.config.Users[0] = user, user, user
	b.ChatID, b.TopicID = 12, 0
	if err := atomicJSON(g.spool.taskPath("alice", b.TaskID), b); err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"last-move","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("last-move", env)
	if err != nil {
		t.Fatal(err)
	}
	s := card.Schedule
	o := RoutineOccurrence{ScheduleID: s.ScheduleID, Revision: s.Revision, DueAt: s.NextDue, JobKind: "reminder", Job: Job{Envelope: s.Envelope, OrganizationID: s.OrganizationID, UserID: s.UserID, ActorID: s.ActorID, ScopeID: s.ScopeID, Channel: s.Channel, ChatID: s.ChatID, TaskID: s.TaskID, TopicID: s.TopicID, Text: s.Input, Trigger: "cron"}}
	if err := g.spool.PutOccurrence(o, s.Envelope); err != nil {
		t.Fatal(err)
	}
	s.LastOccurrenceID = occurrenceID(o)
	if err := atomicJSON(g.spool.schedulePath(s.ScheduleID), s); err != nil {
		t.Fatal(err)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/last-move", fmt.Sprintf(`{"expected_revision":1,"destination_task_id":%q}`, b.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err = g.spool.GetRoutine("last-move", env)
	if err != nil || card.Schedule.LastOccurrenceState != "pending" {
		t.Fatalf("prior last occurrence hidden: %+v %v", card.Schedule, err)
	}
}

func TestRoutineDestinationMoveSendsNextReminderToBoundTopic(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"future-topic","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"topic fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/future-topic", fmt.Sprintf(`{"expected_revision":1,"destination_task_id":%q}`, b.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("future-topic", env)
	if err != nil {
		t.Fatal(err)
	}
	g.tickProactive(card.Schedule.NextDue)
	d, err := g.spool.ClaimDelivery()
	if err != nil || d == nil || d.TaskID != b.TaskID || d.TopicID != 6 || d.ChatID != 11 || d.Text != "topic fixture" {
		t.Fatalf("future delivery escaped new topic: %+v %v", d, err)
	}
}

func TestRoutineDestinationMovePinsNextAgentJobAudience(t *testing.T) {
	g, env, a, b := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"future-agent-topic","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"agent","input":"agent fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/future-agent-topic", fmt.Sprintf(`{"expected_revision":1,"destination_task_id":%q}`, b.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("future-agent-topic", env)
	if err != nil {
		t.Fatal(err)
	}
	g.tickProactive(card.Schedule.NextDue)
	job, err := g.spool.ClaimJob()
	if err != nil || job == nil || job.TaskID != b.TaskID || job.TopicID != 6 || job.ChatID != 11 || job.ConversationID != routineConversation("future-agent-topic") {
		t.Fatalf("future agent job audience escaped destination: %+v %v", job, err)
	}
}

func TestRoutineExplicitDefaultMatchesDiscoveredRootAcrossChats(t *testing.T) {
	g, env, _, b := destinationFixture(t)
	user := g.user("alice")
	user.TelegramIDs = append(user.TelegramIDs, 12)
	g.users[11], g.users[12], g.config.Users[0] = user, user, user
	b.ChatID, b.TopicID = 12, 0
	if err := atomicJSON(g.spool.taskPath("alice", b.TaskID), b); err != nil {
		t.Fatal(err)
	}
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"root-move","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, b.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/root-move", `{"expected_revision":1,"destination_task_id":"default"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	card, err := g.spool.GetRoutine("root-move", env)
	if err != nil || card.Schedule.TaskID != "default" || card.Schedule.ChatID != 11 || card.Schedule.TopicID != 0 {
		t.Fatalf("explicit default differs from discovered root: %+v %v", card.Schedule, err)
	}
}

func TestRoutineDestinationCardMarksRevokedChatUnavailable(t *testing.T) {
	g, _, a, _ := destinationFixture(t)
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"revoked-chat","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"reminder","input":"fixture"}`, a.TaskID))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	g.config.Users[0].TelegramIDs = []int64{12}
	w = destinationRequest(g, http.MethodGet, "/v1/routines/revoked-chat", "")
	var card RoutineCard
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &card) != nil || card.Destination == nil || card.Destination.State != "unavailable" {
		t.Fatalf("revoked chat advertised as live: %d %+v", w.Code, card.Destination)
	}
	w = destinationRequest(g, http.MethodPost, "/v1/routines/revoked-chat/pause", `{"expected_revision":1}`)
	card = RoutineCard{}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &card) != nil || card.Destination == nil || card.Destination.State != "unavailable" {
		t.Fatalf("mutation card advertised revoked chat as live: %d %+v", w.Code, card.Destination)
	}
}

func TestRoutineDestinationCreateRevalidatesAfterProbe(t *testing.T) {
	g, env, a, _ := destinationFixture(t)
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		a.Archived = true
		if err := atomicJSON(g.spool.taskPath("alice", a.TaskID), a); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte("ready"))
	}))
	defer probe.Close()
	g.watchHTTP = probe.Client()
	w := destinationRequest(g, http.MethodPost, "/v1/routines", fmt.Sprintf(`{"schedule_id":"probe-race","destination_task_id":%q,"timezone":"UTC","expression":"0 10 * * 5","job_kind":"check","input":"fixture","expect":"changed","probe":{"kind":"http","url":%q}}`, a.TaskID, probe.URL))
	if w.Code != 400 {
		t.Fatalf("archived during baseline was created: %d %s", w.Code, w.Body.String())
	}
	if _, err := g.spool.GetRoutine("probe-race", env); err == nil {
		t.Fatal("archived target persisted")
	}
}
