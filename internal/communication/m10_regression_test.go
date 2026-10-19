package communication

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestM10RegressionMockAttachmentNeverCallsTelegram(t *testing.T) {
	c := testConfig(t)
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	g.api = api
	if err := g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	blob, err := g.spool.WriteDeliveryBlob("audit-mock", "a.txt", []byte("private fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.spool.EnqueueDelivery(Delivery{ID: "audit-mock", Channel: "mock", ChatID: 42, Text: "file", Artifacts: []hubruntime.ArtifactRef{{Name: "a.txt", Mime: "text/plain", Blob: blob}}}); err != nil {
		t.Fatal(err)
	}
	g.deliverOne(context.Background())
	if len(api.docs)+len(api.photos)+len(api.sent) != 0 {
		t.Fatalf("MOCK CALLED TELEGRAM: documents=%v messages=%v", api.docs, api.sent)
	}
}
func TestM10RegressionRejectedForeignCheckLeavesBaselineUnchanged(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	g.spool = s
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, {ID: "bob", Enabled: true, TelegramIDs: []int64{22}}}
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("replacement")) }))
	defer src.Close()
	_, err := s.CreateSchedule(Schedule{ScheduleID: "alice-watch", Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "private watch", JobKind: "watcher", Probe: &WatchProbe{URL: src.URL}}, o.Job.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWatchBaseline("alice-watch", "original", time.Now()); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"schedule_id":"alice-watch","timezone":"UTC","expression":"0 * * * *","input":"bob check","job_kind":"check","expect":"changed","probe":{"url":%q}}`, src.URL)
	req := httptest.NewRequest("POST", "/v1/routines", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "bob")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("expected rejected duplicate, got %d: %s", w.Code, w.Body.String())
	}
	st, err := s.loadWatchStateLocked("alice-watch")
	if err != nil {
		t.Fatal(err)
	}
	if st.Value != "original" {
		t.Fatalf("FOREIGN BASELINE MODIFIED DESPITE HTTP %d: %q", w.Code, st.Value)
	}
}
func TestM10RegressionCheckMissingProbeDoesNotPanic(t *testing.T) {
	_, _, g := occurrenceFixture(t)
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}}
	req := httptest.NewRequest("POST", "/v1/routines", strings.NewReader(`{"job_kind":"check"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "alice")
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("MALFORMED REQUEST PANICKED: %v", p)
		}
	}()
	g.Handler().ServeHTTP(httptest.NewRecorder(), req)
}
func TestM10RegressionDeletedReminderDoesNotDispatch(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	due := time.Now().UTC().Add(time.Minute)
	_, err := s.CreateSchedule(Schedule{ScheduleID: "cancel-me", Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "once:" + due.Format(time.RFC3339), Input: "do not deliver", JobKind: "reminder"}, o.Job.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TickSchedules(due.Add(time.Second), "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule("cancel-me", o.Job.Envelope); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(due.Add(time.Second), g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil {
		t.Fatal(err)
	}
	if d != nil {
		t.Fatalf("CANCELLED REMINDER STILL DELIVERED: %s", d.Text)
	}
}
func TestM10RegressionUsageCountsSessionDelta(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	counter := int64(10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(hubruntime.SessionUsage{SessionFound: true, SessionID: "same-session", InputTokens: &counter})
	}))
	defer srv.Close()
	runner := HTTPRunner{URL: srv.URL, Spool: s}
	job := o.Job
	job.ID = "job-one"
	runner.meterUsage(context.Background(), job, "run-one")
	counter = 25
	job.ID = "job-two"
	runner.meterUsage(context.Background(), job, "run-two")
	totals, err := s.UsageTotals("user:alice")
	if err != nil {
		t.Fatal(err)
	}
	if totals.InputTokens != 25 {
		t.Fatalf("CUMULATIVE COUNTER ADDED TWICE: want 25, got %d", totals.InputTokens)
	}
}
func TestM10RegressionDigestIncludesSameTickChange(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	due := time.Now().UTC()
	caller := o.Job.Envelope
	_, err := s.CreateSchedule(Schedule{ScheduleID: "watch", Envelope: caller, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "UTC", Expression: "0 * * * *", Input: "watch title", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.invalid"}}, caller, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SeedWatchBaseline("watch", "old", due.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	w := o
	w.ScheduleID = "watch"
	w.DueAt = due
	w.JobKind = "watcher"
	d := o
	d.ScheduleID = "digest"
	d.DueAt = due
	d.JobKind = "digest"
	d.Job.Text = "daily brief"
	for _, oc := range []RoutineOccurrence{w, d} {
		if err := s.PutOccurrence(oc, caller); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DispatchDueOccurrences(due, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(due, func(context.Context, string) ([]byte, error) { return []byte("new-value"), nil }); err != nil {
		t.Fatal(err)
	}
	d.DueAt = due.Add(time.Minute)
	if err := s.PutOccurrence(d, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(d.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "outbox", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(s.root, "outbox", "pending", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var delivery Delivery
		if err := json.Unmarshal(b, &delivery); err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(delivery.ID, "digest-") && strings.Contains(delivery.Text, "new-value") {
			found = true
		}
	}
	if !found {
		t.Fatal("CHANGE LOST FROM BOTH CURRENT AND NEXT DIGEST")
	}
}
func TestM10RegressionMockScheduleRejectsForeignChat(t *testing.T) {
	_, _, g := occurrenceFixture(t)
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, {ID: "bob", Enabled: true, TelegramIDs: []int64{22}}}
	if err := g.mockLinkClaim(42, "alice"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/routines", strings.NewReader(`{"schedule_id":"bob-to-alice","channel":"mock","chat_id":42,"timezone":"UTC","expression":"0 * * * *","input":"foreign message","job_kind":"reminder"}`))
	req.Header.Set("Authorization", "Bearer tok")
	req.Header.Set("X-Hub-Principal", "bob")
	w := httptest.NewRecorder()
	g.Handler().ServeHTTP(w, req)
	if w.Code == 200 {
		t.Fatal("BOB CAN SCHEDULE INTO ALICE MOCK CHAT")
	}
}
func TestM10RegressionMockClientIDIsOwnerScoped(t *testing.T) {
	_, _, g := occurrenceFixture(t)
	g.config.ControlAuth = "tok"
	g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, {ID: "bob", Enabled: true, TelegramIDs: []int64{22}}}
	for i, p := range []string{"alice", "bob"} {
		req := httptest.NewRequest("POST", "/v1/mock/message", strings.NewReader(fmt.Sprintf(`{"chat_id":%d,"text":"hello","client_id":"same-id"}`, 42+i)))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("X-Hub-Principal", p)
		w := httptest.NewRecorder()
		g.Handler().ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("INDEPENDENT OWNER CLIENT ID COLLIDES: %s status %d %s", p, w.Code, w.Body.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(g.spool.root, "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("TWO OWNERS GOT HTTP 200 BUT ONLY %d JOB EXISTS", len(entries))
	}
}
func TestM10RegressionKeyboardPreservesForumTopic(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"ok":true,"result":{}}`)
	}))
	defer srv.Close()
	api := &telegramAPI{baseURL: srv.URL, token: "fixture", client: srv.Client()}
	if err := api.SendKeyboard(context.Background(), -100, 77, "approval", []DeliveryButton{{Text: "yes", Data: "a:fixture"}}); err != nil {
		t.Fatal(err)
	}
	if sent["message_thread_id"] != float64(77) {
		t.Fatalf("KEYBOARD LOST TOPIC: got %v", sent["message_thread_id"])
	}
}
