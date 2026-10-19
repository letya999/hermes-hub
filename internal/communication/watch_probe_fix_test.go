package communication

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
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

// A crash between the fetch and the settle leaves a probing occurrence plus a
// durable probe-result receipt. The next pass replays the receipt instead of
// re-fetching — the event is not lost, and a repeated replay cannot duplicate
// the delivery (SPEC-0048 crash-safety).
func TestProbeResultReceiptReplaysAfterCrash(t *testing.T) {
	s, o, g := occurrenceFixture(t)
	caller := o.Job.Envelope
	if _, err := s.CreateSchedule(Schedule{ScheduleID: "w-crash", Envelope: caller, OrganizationID: o.Job.OrganizationID, UserID: o.Job.UserID, ActorID: o.Job.ActorID, ScopeID: o.Job.ScopeID, Channel: "telegram_bot", ChatID: o.Job.ChatID, Timezone: "UTC", Expression: "0 * * * *", Input: "watch the page", JobKind: "watcher", Probe: &WatchProbe{URL: "https://example.com/p"}}, caller, ""); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	o.ScheduleID = "w-crash"
	o.JobKind = "watcher"
	if err := s.PutOccurrence(o, caller); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchDueOccurrences(o.DueAt, g.authorizeOccurrence); err != nil {
		t.Fatal(err)
	}
	// The receipt records exactly what settleProbe produced before dying: the
	// changed value, the dispatched occurrence and the queued delivery.
	o.State = "dispatched"
	delivery, err := normalizeDelivery(Delivery{ID: "watch-" + o.Job.ID, OccurrenceID: o.Job.ID, Channel: o.Job.Channel, ChatID: o.Job.ChatID, Text: "watch the page\nhttps://example.com/p\nwas: v1\nnow: v2", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	receipt := probeResult{
		Watch:      WatchState{ScheduleID: "w-crash", Baseline: watchHash("v2"), Value: "v2", PrevValue: "v1", ChangedAt: now, LastSuccess: now, Generation: 1},
		Occurrence: o,
		Delivery:   &delivery,
	}
	receiptPath := filepath.Join(s.root, "occurrences", occurrenceID(o)+".probe-result")
	if err := atomicJSON(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	// Replay must not touch the source again.
	fetch := func(context.Context, string) ([]byte, error) {
		t.Fatal("receipt replay re-fetched the source")
		return nil, nil
	}
	if err := s.ProbeDueOccurrences(now, fetch); err != nil {
		t.Fatal(err)
	}
	d, err := s.ClaimDelivery()
	if err != nil || d == nil || !strings.Contains(d.Text, "v2") {
		t.Fatalf("receipt delivery lost: %+v %v", d, err)
	}
	if extra, _ := s.ClaimDelivery(); extra != nil {
		t.Fatalf("receipt replay duplicated the delivery: %+v", extra)
	}
	b, err := os.ReadFile(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved RoutineOccurrence
	if json.Unmarshal(b, &saved) != nil || saved.State != "dispatched" {
		t.Fatalf("replayed occurrence not settled: %+v", saved)
	}
	wb, err := os.ReadFile(s.watchPath("w-crash"))
	if err != nil {
		t.Fatal(err)
	}
	var st WatchState
	if json.Unmarshal(wb, &st) != nil || st.Value != "v2" || st.Baseline != watchHash("v2") {
		t.Fatalf("replayed watch state: %+v", st)
	}
	// A second crash window — between the delivery enqueue and the occurrence
	// write — replays the same receipt again: the delivery id dedupes, so the
	// recovery path never produces a duplicate notification.
	saved.State = "probing"
	if err := atomicJSON(filepath.Join(s.root, "occurrences", occurrenceID(o)+".json"), saved); err != nil {
		t.Fatal(err)
	}
	if err := s.ProbeDueOccurrences(now, fetch); err != nil {
		t.Fatal(err)
	}
	if extra, _ := s.ClaimDelivery(); extra != nil {
		t.Fatalf("re-replay duplicated the delivery: %+v", extra)
	}
}
