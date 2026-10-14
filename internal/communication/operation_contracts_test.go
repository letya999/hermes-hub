package communication

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPReminderUsesServerClockAndReplay(t *testing.T) {
	c := testConfig(t)
	c.ControlAuth = "control-token"
	g, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(24 * time.Hour)
	g.now = func() time.Time { return now }
	post := func(text string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/routines", strings.NewReader(text))
		req.Header.Set("Authorization", "Bearer control-token")
		req.Header.Set("X-Hub-Principal", "alice")
		rec := httptest.NewRecorder()
		g.Handler().ServeHTTP(rec, req)
		return rec
	}
	body := `{"schedule_id":"http-relative","timezone":"UTC","expression":"after:180s","input":"exact message","job_kind":"reminder"}`
	first := post(body)
	var saved Schedule
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &saved) != nil {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	if !saved.NextDue.Equal(now.Add(180 * time.Second)) {
		t.Errorf("server clock ignored: due=%v now=%v", saved.NextDue, now)
	}
	now = now.Add(time.Hour)
	replayed := post(body)
	if replayed.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replayed.Code, replayed.Body.String())
	}
	var current Schedule
	if json.Unmarshal(replayed.Body.Bytes(), &current) != nil || !current.NextDue.Equal(saved.NextDue) {
		t.Fatal("HTTP replay moved due")
	}
	conflict := post(strings.Replace(body, "exact message", "different message", 1))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestRelativeReminderCreationReplayAndRetirement(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	draft := Schedule{ScheduleID: "relative-reminder", Envelope: o.Job.Envelope, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "after:180s", Input: "REMINDER-test", JobKind: "reminder"}
	before := time.Now().UTC()
	created, err := s.CreateSchedule(draft, draft.Envelope, "")
	if err != nil {
		t.Fatal(err)
	}
	if !created.NextDue.After(before.Add(179*time.Second)) || !created.NextDue.Before(time.Now().Add(181*time.Second)) || created.Expression == draft.Expression {
		t.Fatalf("relative deadline was not resolved once: %+v", created)
	}
	replay, err := s.CreateSchedule(draft, draft.Envelope, "")
	if err != nil || !replay.NextDue.Equal(created.NextDue) || replay.Revision != created.Revision {
		t.Fatalf("replay changed deadline: %+v %v", replay, err)
	}
	changed := draft
	changed.Input = "different intent"
	if _, err := s.CreateSchedule(changed, draft.Envelope, ""); err == nil {
		t.Fatal("changed intent accepted")
	}
	if err := s.DeleteSchedule(draft.ScheduleID, draft.Envelope); err != nil {
		t.Fatal(err)
	}
	if retired, err := s.CreateSchedule(draft, draft.Envelope, ""); !errors.Is(err, ErrScheduleConflict) || retired.ScheduleID != "" {
		t.Fatalf("retired replay reactivated: %+v %v", retired, err)
	}
	b, err := os.ReadFile(s.cancelPath(draft.ScheduleID))
	if err != nil {
		t.Fatal(err)
	}
	var tombstone Schedule
	if json.Unmarshal(b, &tombstone) != nil || tombstone.Creation == nil || tombstone.Creation.IntentSHA256 == "" {
		t.Fatal("tombstone lost the creation receipt")
	}
	for _, expr := range []string{"after:0s", "after:1.5s", "after:8761h", "after:-1s"} {
		draft.ScheduleID = "invalid-delay"
		draft.Expression = expr
		if _, err := s.CreateSchedule(draft, draft.Envelope, ""); err == nil {
			t.Errorf("bad delay accepted: %s", expr)
		}
	}
}

func TestConcurrentIdenticalCreatesCommitOnce(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	draft := Schedule{ScheduleID: "raced-reminder", Envelope: o.Job.Envelope, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "after:180s", Input: "raced", JobKind: "reminder"}
	var wg sync.WaitGroup
	results := make(chan Schedule, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := s.CreateSchedule(draft, draft.Envelope, "")
			if err != nil {
				t.Error(err)
				return
			}
			results <- created
		}()
	}
	wg.Wait()
	close(results)
	var creates []Schedule
	for created := range results {
		creates = append(creates, created)
	}
	if len(creates) != 2 || !creates[0].NextDue.Equal(creates[1].NextDue) {
		t.Fatalf("concurrent creates diverged: %+v", creates)
	}
}

func TestWatcherRejectsInvisibleURLCharacters(t *testing.T) {
	_, o, _ := occurrenceFixture(t)
	for _, raw := range []string{"https://example.com/time\u2060", "https://example.com/\u200btime"} {
		draft := Schedule{ScheduleID: "invisible-watch", Envelope: o.Job.Envelope, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: raw}}
		if err := ValidateSchedule(draft, draft.Envelope, time.Now()); err == nil {
			t.Errorf("invisible URL accepted: %q", raw)
		}
	}
}

func TestWatcherURLWhitespaceAndInvisibleCharacters(t *testing.T) {
	s, o, _ := occurrenceFixture(t)
	draft := Schedule{ScheduleID: "clean-watch", Envelope: o.Job.Envelope, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: o.Job.Channel, ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "* * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "  https://example.com/time  "}}
	created, err := s.CreateSchedule(draft, draft.Envelope, "")
	if err != nil || created.Probe.URL != "https://example.com/time" {
		t.Fatalf("surrounding whitespace: %+v %v", created, err)
	}
	for _, bad := range []string{"https://example.com/time\u2060", "https://example.com/\u200btime", "https://user:pass@example.com/", "https://example.com/\npath"} {
		draft.Probe = &WatchProbe{URL: bad}
		if err := ValidateSchedule(draft, draft.Envelope, time.Now()); err == nil {
			t.Errorf("bad URL accepted: %q", bad)
		}
	}
}
