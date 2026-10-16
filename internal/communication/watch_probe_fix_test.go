package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// An alternation regex is a common watcher shape ("field A or, failing that,
// field B"): whichever branch matches first wins, so the captured value must
// come from the group that actually matched, never a fixed group index.
func TestWatchValuePicksNonEmptyGroup(t *testing.T) {
	re := regexp.MustCompile(`(?:edited":(\d+)|<title>(.*?)</title>)`)
	body := []byte(`<html><head><title>Notion</title></head><body>edited":1700000000</body></html>`)
	if v := watchValue(body, re); v != "Notion" {
		t.Fatalf("title branch matched but group selection returned %q", v)
	}
	body = []byte(`edited":1700000000 then <title>Notion</title>`)
	if v := watchValue(body, re); v != "1700000000" {
		t.Fatalf("first branch value lost: %q", v)
	}
}

// Real-world pages — a GitHub issues list is ~400 KB, vc.ru ~500 KB — must fit
// the deterministic probe budget. The bound exists to keep polls cheap, not to
// reject ordinary pages.
func TestFetchWatchAcceptsRealPageSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 500*1024))
	}))
	defer server.Close()
	g := &Gateway{watchHTTP: server.Client()}
	body, err := g.fetchWatch(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("500KB page rejected: %v", err)
	}
	if len(body) != 500*1024 {
		t.Fatalf("body truncated: %d", len(body))
	}
}

// A failing probe leaves the owner blind unless the recorded error says why:
// "probe status 302" or "probe body exceeds size limit" lets the agent fix the
// watcher instead of silently retrying forever.
func TestProbeErrorRecordedVerbatim(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-err", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/p", Regex: "x: (\\d+)"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	o.ScheduleID = "w-err"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	fetch := func(context.Context, string) ([]byte, error) { return nil, errors.New("probe status 302") }
	if err := s.ProbeDueOccurrences(o.DueAt, fetch); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.watchPath("w-err"))
	if err != nil {
		t.Fatal(err)
	}
	var st WatchState
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.LastError, "probe status 302") {
		t.Fatalf("real probe error not recorded: %q", st.LastError)
	}
}
