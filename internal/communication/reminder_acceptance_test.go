package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

func reminderSchedule(t *testing.T, s *Spool, o RoutineOccurrence, expression string, window time.Duration) Schedule {
	t.Helper()
	due := time.Date(2100, time.January, 1, 7, 0, 0, 0, time.UTC) // Friday 10:00 Moscow.
	draft := Schedule{ScheduleID: "reminder-acceptance", Envelope: o.Job.Envelope, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", ChatID: 11, Timezone: "Europe/Moscow", Expression: expression, Input: "Оплатить счёт — ровно этот текст", JobKind: "reminder", CatchUpWindow: window}
	created, err := s.createScheduleAt(draft, draft.Envelope, "", nil, due.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !created.NextDue.Equal(due) {
		t.Fatalf("Friday 10:00 Moscow: got %v, want %v", created.NextDue, due)
	}
	return created
}

func TestWeeklyReminderRecoversCurrentSlotAfterExpiredPending(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	created := reminderSchedule(t, s, o, "0 10 * * 5", 0)
	// Crash after saving last Friday's occurrence, before dispatch.
	if err := s.TickSchedules(created.NextDue, "", g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	g.spool = restarted
	thisFriday := time.Date(2100, time.January, 8, 7, 0, 0, 0, time.UTC)
	g.tickProactive(thisFriday.Add(30 * time.Minute))
	d, err := restarted.ClaimDelivery()
	wantID := occurrenceID(RoutineOccurrence{ScheduleID: created.ScheduleID, Revision: created.Revision, DueAt: thisFriday})
	if err != nil || d == nil || d.Text != created.Input || d.OccurrenceID != wantID {
		t.Fatalf("current Friday must deliver while last Friday expires: %+v %v", d, err)
	}
	items, err := restarted.ListSchedules(o.Job.Envelope)
	wantNext := time.Date(2100, time.January, 15, 7, 0, 0, 0, time.UTC)
	if err != nil || len(items) != 1 || !items[0].NextDue.Equal(wantNext) {
		t.Fatalf("weekly cadence lost after recovery: %+v %v", items, err)
	}
}

func TestReminderHistoryDoesNotExposeForeignDeliveryAfterRetention(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	created := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", 0)
	g.tickProactive(created.NextDue)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil {
		t.Fatalf("Alice delivery setup: %+v %v", d, err)
	}
	if err := s.CompleteDelivery(d.ID); err != nil {
		t.Fatal(err)
	}
	bob := identity.TelegramEnvelope("bob", 22, "bob", "policy-1")
	draft := created
	draft.ScheduleID, draft.Envelope = "bob-reminder", bob
	draft.UserID, draft.ActorID, draft.ScopeID, draft.ChatID = "bob", "bob", "user:bob", 22
	draft.LastOccurrenceID = d.OccurrenceID
	bobCreated, err := s.createScheduleAt(draft, bob, "", nil, created.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if bobCreated.LastOccurrenceID != "" {
		t.Fatal("creation accepted a caller-supplied server history reference")
	}
	// Legacy records may already contain that reference; reads must still refuse it.
	bobCreated.LastOccurrenceID = d.OccurrenceID
	if err := atomicJSON(s.schedulePath(bobCreated.ScheduleID), bobCreated); err != nil {
		t.Fatal(err)
	}
	// The detail archive expires before the completed outbox receipt.
	if err := os.Remove(filepath.Join(s.root, "occurrences", d.OccurrenceID+".json")); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSchedules(bob)
	if err != nil || len(items) != 1 {
		t.Fatalf("Bob owner read: %+v %v", items, err)
	}
	if items[0].LastDeliveryState != "" || items[0].LastOccurrenceState != "unavailable" {
		t.Fatalf("foreign receipt exposed without verified occurrence ownership: %+v", items[0])
	}
}

func TestReminderIsNotStarvedByExpiredOccurrenceBacklog(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	created := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", 0)
	for i := range 32 {
		old := o
		old.ScheduleID = fmt.Sprintf("expired-%d", i)
		old.DueAt = created.NextDue.Add(-2*time.Hour + time.Duration(i)*time.Second)
		if err := s.PutOccurrence(old, old.Job.Envelope); err != nil {
			t.Fatal(err)
		}
	}
	g.tickProactive(created.NextDue)
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || d.Text != created.Input {
		t.Fatalf("fresh reminder starved behind expired backlog: %+v %v", d, err)
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "occurrences"))
	if err != nil {
		t.Fatal(err)
	}
	settled := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(s.root, "occurrences", entry.Name()))
		var saved RoutineOccurrence
		if err != nil || json.Unmarshal(body, &saved) != nil {
			t.Fatalf("invalid persisted occurrence: %v", err)
		}
		if saved.State != "pending" {
			settled++
		}
	}
	// tickProactive has two dispatch passes, each bounded to 16 settlements.
	if settled > 32 {
		t.Fatalf("dispatch bound widened: %d settled", settled)
	}
}

func TestReminderCommandShowsOutcomeWithoutAgent(t *testing.T) {
	for _, tc := range []struct{ outcome, want string }{{"missed", "срабатывание пропущено"}, {"uncertain", "доставка не подтверждена"}, {"delivered", "доставлено"}} {
		t.Run(tc.outcome, func(t *testing.T) {
			c := testConfig(t)
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			created := reminderSchedule(t, g.spool, RoutineOccurrence{Job: Job{Envelope: c.Users[0].envelope(11)}}, "once:2100-01-01T10:00:00+03:00", 0)
			g.api = &fakeAPI{}
			if tc.outcome == "uncertain" {
				g.api = &fakeAPI{err: errors.New("unknown transport result")}
			}
			now := created.NextDue
			if tc.outcome == "missed" {
				now = now.Add(2 * time.Hour)
			}
			g.now = func() time.Time { return now }
			g.tickProactive(now)
			g.deliverOne(context.Background())
			if err := g.handleUpdate(context.Background(), taskUpdate(1, 1, "/routines", 11, 0)); err != nil {
				t.Fatal(err)
			}
			reply, err := g.spool.ClaimDelivery()
			if err != nil || reply == nil || !strings.Contains(reply.Text, created.ScheduleID) || !strings.Contains(reply.Text, tc.want) {
				t.Fatalf("/routines must show %s: %+v %v", tc.want, reply, err)
			}
			if job, err := g.spool.ClaimJob(); err != nil || job != nil {
				t.Fatalf("listing reminder outcome woke agent: %+v %v", job, err)
			}
		})
	}
}

func TestReminderGatewayWithoutRuntimeAndLostTransportAnswer(t *testing.T) {
	for _, outcome := range []string{"delivered", "lost-answer", "cancelled", "revoked"} {
		t.Run(outcome, func(t *testing.T) {
			var runtimeCalls, modelCalls, sends atomic.Int32
			runtimeAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				runtimeCalls.Add(1)
				http.Error(w, "runtime unavailable", http.StatusServiceUnavailable)
			}))
			defer runtimeAPI.Close()
			modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				modelCalls.Add(1)
				http.Error(w, "model unavailable", http.StatusServiceUnavailable)
			}))
			defer modelAPI.Close()
			t.Setenv("OPENAI_BASE_URL", modelAPI.URL)
			payloads := make(chan map[string]any, 4)
			telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bottoken/sendMessage" {
					t.Errorf("unexpected Bot API request: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				sends.Add(1)
				payloads <- body
				if outcome == "lost-answer" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":17}}`))
			}))
			defer telegram.Close()
			c := testConfig(t)
			c.RuntimeURL, c.RuntimeAuth, c.APIBaseURL = runtimeAPI.URL, "synthetic-runtime", telegram.URL
			g, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			envelope := c.Users[0].envelope(11)
			created := reminderSchedule(t, g.spool, RoutineOccurrence{Job: Job{Envelope: envelope}}, "once:2100-01-01T10:00:00+03:00", 0)
			g.now = func() time.Time { return created.NextDue }
			g.tickProactive(created.NextDue)
			if outcome == "cancelled" {
				if err := g.spool.DeleteSchedule(created.ScheduleID, envelope); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "revoked" {
				g.config.Users[0].Enabled = false
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); g.worker(ctx) }()
			defer func() { cancel(); <-done }()
			deliveryDir := "failed"
			if outcome == "delivered" {
				deliveryDir = "done"
			}
			id := occurrenceID(RoutineOccurrence{ScheduleID: created.ScheduleID, Revision: created.Revision, DueAt: created.NextDue})
			deadline := time.Now().Add(5 * time.Second)
			for {
				_, err = os.Stat(filepath.Join(c.SpoolDir, "outbox", deliveryDir, "reminder-"+id+".json"))
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			cancel()
			<-done
			// The deferred join is still safe after this explicit stop.
			if err != nil {
				t.Fatalf("delivery never settled in %s: %v", deliveryDir, err)
			}
			wantSends := int32(1)
			if outcome == "cancelled" || outcome == "revoked" {
				wantSends = 0
			}
			if sends.Load() != wantSends || runtimeCalls.Load() != 0 || modelCalls.Load() != 0 {
				t.Fatalf("send/runtime/model calls = %d/%d/%d; want %d/0/0", sends.Load(), runtimeCalls.Load(), modelCalls.Load(), wantSends)
			}
			if wantSends == 1 {
				body := <-payloads
				if body["text"] != created.Input || body["chat_id"] != float64(11) {
					t.Fatalf("saved text or audience changed: %+v", body)
				}
			}
			restarted, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			restarted.tickProactive(created.NextDue)
			restarted.deliverOne(context.Background())
			if sends.Load() != wantSends {
				t.Fatalf("restart repeated a completed or uncertain send: %d", sends.Load())
			}
			if job, err := restarted.spool.ClaimJob(); err != nil || job != nil {
				t.Fatalf("reminder woke agent: %+v %v", job, err)
			}
		})
	}
}

func TestReminderOwnerHTTPStatusAndIsolation(t *testing.T) {
	for _, outcome := range []string{"missed", "uncertain", "delivered"} {
		t.Run(outcome, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			created := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", 0)
			g.config.Users = []User{{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, {ID: "bob", Enabled: true, TelegramIDs: []int64{22}}}
			g.config.ControlTokensFile = filepath.Join(t.TempDir(), "enrolled.json")
			tokens, err := json.Marshal(map[string]identity.Envelope{"alice-session": o.Job.Envelope, "bob-session": identity.TelegramEnvelope("bob", 22, "bob", "policy-1")})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(g.config.ControlTokensFile, tokens, 0600); err != nil {
				t.Fatal(err)
			}
			g.api = &fakeAPI{}
			if outcome == "uncertain" {
				g.api = &fakeAPI{err: errors.New("lost transport answer")}
			}
			now := created.NextDue
			if outcome == "missed" {
				now = now.Add(2 * time.Hour)
			}
			g.now = func() time.Time { return now }
			g.tickProactive(now)
			g.deliverOne(context.Background())
			call := func(token, method, path, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("X-Hub-Principal", "alice") // Bob's token must override this claim.
				w := httptest.NewRecorder()
				g.Handler().ServeHTTP(w, r)
				return w
			}
			before, err := os.ReadFile(s.schedulePath(created.ScheduleID))
			if err != nil {
				t.Fatal(err)
			}
			base := "/v1/routines/" + created.ScheduleID
			for _, attack := range []struct {
				method, path, body string
				status             int
			}{
				{http.MethodGet, base, "", 404},
				{http.MethodPost, base + "/snooze", `{"until":"2100-01-02T12:00:00Z"}`, 403},
				{http.MethodPost, base, `{"expression":"once:2100-01-02T12:00:00Z"}`, 403},
				{http.MethodDelete, base, "", 403},
			} {
				if got := call("bob-session", attack.method, attack.path, attack.body); got.Code != attack.status {
					t.Fatalf("foreign %s %s: %d %s", attack.method, attack.path, got.Code, got.Body.String())
				}
			}
			if got := call("bob-session", http.MethodGet, "/v1/routines", ""); got.Code != 200 || bytes.Contains(got.Body.Bytes(), []byte(created.ScheduleID)) {
				t.Fatalf("valid Bob session saw Alice schedule: %d %s", got.Code, got.Body.String())
			}
			after, err := os.ReadFile(s.schedulePath(created.ScheduleID))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("foreign session changed schedule: %v", err)
			}
			w := call("alice-session", http.MethodGet, "/v1/routines", "")
			var items []map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &items) != nil || len(items) != 1 {
				t.Fatalf("owner list: %d %s", w.Code, w.Body.String())
			}
			field, want := "last_delivery_state", outcome
			if outcome == "missed" {
				field = "last_occurrence_state"
			}
			if items[0][field] != want {
				t.Fatalf("owner cannot see %s outcome: %s", outcome, w.Body.String())
			}
		})
	}
}

func TestReminderScheduleCatchUpAcrossRestartAndConcurrentTicks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		late       time.Duration
		window     time.Duration
		wantState  string
		wantQueued bool
	}{
		{"on-time", 0, 0, "dispatched", true},
		{"window-edge", time.Hour, 0, "dispatched", true},
		{"outside-window", time.Hour + time.Second, 0, "missed", false},
		{"custom-long-window", 90 * time.Minute, 2 * time.Hour, "dispatched", true},
		{"custom-short-window", 31 * time.Minute, 30 * time.Minute, "missed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			created := reminderSchedule(t, s, o, "once:2100-01-01T10:00:00+03:00", tc.window)
			restarted, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			g.spool = restarted
			now := created.NextDue.Add(tc.late)
			var wg sync.WaitGroup
			for range 12 {
				wg.Go(func() {
					if err := restarted.TickSchedules(now, "", g.authorizeOccurrence); err != nil {
						t.Error(err)
					}
					if err := restarted.DispatchDueOccurrences(now, g.authorizeOccurrence); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			items, err := restarted.ListSchedules(o.Job.Envelope)
			if err != nil || len(items) != 1 || items[0].LastOccurrenceID == "" {
				t.Fatalf("due reminder lost its occurrence: %+v %v", items, err)
			}
			body, err := os.ReadFile(filepath.Join(s.root, "occurrences", items[0].LastOccurrenceID+".json"))
			var occurrence RoutineOccurrence
			if err != nil || json.Unmarshal(body, &occurrence) != nil || occurrence.State != tc.wantState {
				t.Fatalf("occurrence state: %+v %v; want %s", occurrence, err, tc.wantState)
			}
			queued, err := restarted.ClaimDelivery()
			if err != nil || (queued != nil) != tc.wantQueued {
				t.Fatalf("catch-up delivery: %+v %v; want queued=%v", queued, err, tc.wantQueued)
			}
			if queued != nil {
				if queued.Text != created.Input || queued.OccurrenceID != occurrence.Job.ID {
					t.Fatalf("wrong saved text or occurrence: %+v", queued)
				}
				if err := restarted.CompleteDelivery(queued.ID); err != nil {
					t.Fatal(err)
				}
			}
			if extra, err := restarted.ClaimDelivery(); err != nil || extra != nil {
				t.Fatalf("duplicate logical delivery: %+v %v", extra, err)
			}
			if job, err := restarted.ClaimJob(); err != nil || job != nil {
				t.Fatalf("catch-up woke agent: %+v %v", job, err)
			}
		})
	}
}

func TestReminderSnoozeSurvivesRestartAndPreservesWeeklyCadence(t *testing.T) {
	for _, mode := range []string{"once", "weekly", "already-dispatched", "legacy-snooze"} {
		t.Run(mode, func(t *testing.T) {
			s, o, g := occurrenceFixture(t)
			expression := "once:2100-01-01T10:00:00+03:00"
			if mode == "weekly" {
				expression = "0 10 * * 5"
			}
			created := reminderSchedule(t, s, o, expression, 0)
			until := created.NextDue.Add(2 * time.Hour)
			if mode == "already-dispatched" {
				g.tickProactive(created.NextDue)
			}
			if mode == "legacy-snooze" {
				// The previous version saved snooze without moving NextDue.
				legacy := created
				legacy.SnoozedUntil, legacy.Revision = until, created.Revision+1
				if err := atomicJSON(s.schedulePath(created.ScheduleID), legacy); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.SnoozeSchedule(created.ScheduleID, o.Job.Envelope, until); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := NewSpool(s.root)
			if err != nil {
				t.Fatal(err)
			}
			g.spool = restarted
			api := &fakeAPI{}
			g.api = api
			g.now = func() time.Time { return until }
			for _, tick := range []time.Time{created.NextDue, until.Add(-time.Second), until, until} {
				g.tickProactive(tick)
				g.deliverOne(context.Background())
				if tick.Before(until) && len(api.sent) != 0 {
					t.Fatalf("snoozed reminder sent before new deadline: %v", api.sent)
				}
			}
			if len(api.sent) != 1 || api.sent[0] != created.Input {
				t.Fatalf("snoozed reminder must send saved text exactly once: %v", api.sent)
			}
			items, err := restarted.ListSchedules(o.Job.Envelope)
			if err != nil || len(items) != 1 {
				t.Fatalf("schedules: %+v %v", items, err)
			}
			if mode == "weekly" {
				want := time.Date(2100, time.January, 8, 7, 0, 0, 0, time.UTC)
				if !items[0].Enabled || !items[0].NextDue.Equal(want) {
					t.Fatalf("weekly cadence moved: %+v; want %v", items[0], want)
				}
				g.tickProactive(want)
				g.deliverOne(context.Background())
				if len(api.sent) != 2 || api.sent[1] != created.Input {
					t.Fatalf("next Friday did not deliver: %v", api.sent)
				}
			} else if items[0].Enabled || !items[0].NextDue.IsZero() {
				t.Fatalf("one-shot reminder stayed enabled: %+v", items[0])
			}
			if job, err := restarted.ClaimJob(); err != nil || job != nil {
				t.Fatalf("reminder entered agent queue: %+v %v", job, err)
			}
		})
	}
}
