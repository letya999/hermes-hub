package automation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func grantDreaming(t *testing.T, h *harness, principal string) {
	t.Helper()
	h.store.RightsDoc.Capabilities["memory"] = api.RightCapability{Description: "fixture", Operations: map[string]api.RightOperation{
		"dreaming": {Subject: "self", Scope: "none", Issuers: []string{"operator"}, Effect: "write"},
	}}
	h.seq++
	grant := api.NewRightGrant("dreaming-"+principal, principal, "memory", "dreaming", "", "fixture", "operator", h.now.Add(time.Hour))
	if err := api.Confirm(&grant, "operator", h.now); err != nil {
		t.Fatal(err)
	}
	if err := h.gov.GrantRight(grant, h.store.RightsDoc, h.now); err != nil {
		t.Fatal(err)
	}
}

func TestDreamingSystemRoutineUsesEachLocalDayAndIsProtected(t *testing.T) {
	h := newHarness(t)
	for _, principal := range []string{"alice", "bob"} {
		h.grant(t, principal, "schedule")
		grantDreaming(t, h, principal)
	}
	host := h.host(t)
	host.cfg.DreamingPlan = func(auth identity.Envelope) (night.Schedule, error) {
		plan := night.Schedule{Enabled: true, LocalTime: "02:30", Timezone: "UTC", Revision: "rev-1"}
		if auth.PrincipalID == "bob" {
			plan.Timezone = "America/New_York"
		}
		return plan, nil
	}
	host.cfg.DreamingRun = func(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error) {
		return "", nil
	}
	for _, principal := range []string{"alice", "bob"} {
		if _, err := host.engineFor(principal); err != nil {
			t.Fatal(err)
		}
	}
	// New York skips 02:30 on this spring transition. The first valid
	// post-gap clock tick still owns exactly one local-date occurrence.
	before := time.Date(2026, 3, 8, 6, 59, 0, 0, time.UTC)
	if err := host.Tick(context.Background(), before); err != nil {
		t.Fatal(err)
	}
	aliceRoot := filepath.Join(host.ownerRoot("alice"), "system")
	bobRoot := filepath.Join(host.ownerRoot("bob"), "system")
	if _, err := os.Stat(filepath.Join(aliceRoot, "dreaming", "2026-03-08.json")); err != nil {
		t.Fatalf("alice's due local day missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bobRoot, "dreaming", "2026-03-08.json")); !os.IsNotExist(err) {
		t.Fatalf("bob ran before his local time: %v", err)
	}
	after := time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := host.Tick(context.Background(), after); err != nil {
			t.Fatal(err)
		}
	}
	var day dreamingDay
	if err := loadJSON(filepath.Join(bobRoot, "dreaming", "2026-03-08.json"), &day); err != nil || day.PrincipalID != "bob" || day.LeafDate != "2026-03-07" || day.State != "complete" {
		t.Fatalf("bad bob date key or leaf: %+v %v", day, err)
	}
	entries, err := os.ReadDir(filepath.Join(bobRoot, "dreaming"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("duplicate local day: %d %v", len(entries), err)
	}
	if rows, err := host.ListSchedules(context.Background(), h.envelope(t, "bob")); err != nil || len(rows) != 0 {
		t.Fatalf("system routine leaked into ordinary CRUD: %+v %v", rows, err)
	}
	if err := host.PauseSchedule(context.Background(), h.envelope(t, "bob"), routines.SystemDreamingID); err == nil {
		t.Fatal("ordinary CRUD paused protected system routine")
	}
	var routine commodel.Schedule
	if err := loadJSON(filepath.Join(host.ownerRoot("bob"), "schedules", routines.SystemDreamingID+".json"), &routine); err != nil || routine.ScheduleID != routines.SystemDreamingID || routine.Envelope.PrincipalID != "bob" || routine.Timezone != "America/New_York" || routine.JobKind != "dreaming" {
		t.Fatalf("protected routine changed: %+v %v", routine, err)
	}
	if routine.LastOccurrenceID == "" {
		t.Fatal("shared routine scheduler did not register a dreaming occurrence")
	}
	var occurrence commodel.RoutineOccurrence
	if err := loadJSON(filepath.Join(host.ownerRoot("bob"), "occurrences", routine.LastOccurrenceID+".json"), &occurrence); err != nil || occurrence.JobKind != "dreaming" || occurrence.State != "dispatched" {
		t.Fatalf("dreaming occurrence not settled through routine engine: %+v %v", occurrence, err)
	}
	// A new Host over the same owner files does not mint another occurrence.
	restarted := h.host(t)
	restarted.cfg.DreamingPlan = host.cfg.DreamingPlan
	restarted.cfg.DreamingRun = host.cfg.DreamingRun
	if _, err := restarted.engineFor("bob"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), after); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(filepath.Join(bobRoot, "dreaming"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("restart duplicated local day: %d %v", len(entries), err)
	}
}

func TestDreamingRunRetriesAndQueuesDigestOnce(t *testing.T) {
	h := newHarness(t)
	grantDreaming(t, h, "alice")
	host := h.host(t)
	auth := h.envelope(t, "alice")
	if _, err := host.engineFor("alice"); err != nil {
		t.Fatal(err)
	}
	host.cfg.DreamingPlan = func(identity.Envelope) (night.Schedule, error) {
		return night.Schedule{Enabled: true, LocalTime: "04:00", Timezone: "UTC", Revision: "r1"}, nil
	}
	host.cfg.RoutingUser = func(string) (commodel.User, error) {
		return commodel.User{ID: "alice", Enabled: true, TelegramIDs: []int64{11}}, nil
	}
	calls, failed := 0, true
	host.cfg.DreamingRun = func(_ context.Context, got identity.Envelope, leaf string, _ night.Schedule, _ locale.Lang, sink func(context.Context, night.IntentProposal) error) (string, error) {
		calls++
		if !got.Equal(auth) || leaf != "2026-11-05" {
			t.Fatalf("wrong run owner/day: %+v %s", got, leaf)
		}
		if failed {
			return "", os.ErrPermission
		}
		if err := sink(context.Background(), night.IntentProposal{Text: "call the bank"}); err != nil {
			return "", err
		}
		return "remembered one thing", nil
	}
	now := time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC)
	if err := host.Tick(context.Background(), now); err == nil {
		t.Fatal("failed run marked done")
	}
	failed = false
	// A failed night retries after its backoff, not on the next tick.
	retry := now.Add(time.Minute)
	if err := host.Tick(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if err := host.Tick(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || h.comm.count() != 1 {
		t.Fatalf("calls=%d deliveries=%d", calls, h.comm.count())
	}
	dayPath := filepath.Join(host.ownerRoot("alice"), "system", "dreaming", "2026-11-06.json")
	var day dreamingDay
	if err := loadJSON(dayPath, &day); err != nil || day.State != "complete" {
		t.Fatalf("day=%+v err=%v", day, err)
	}
	assignments, err := os.ReadDir(filepath.Join(host.ownerRoot("alice"), "assignments"))
	if err != nil || len(assignments) != 1 {
		t.Fatalf("draft count=%d err=%v", len(assignments), err)
	}
	restarted := h.host(t)
	restarted.cfg.DreamingPlan, restarted.cfg.DreamingRun, restarted.cfg.RoutingUser = host.cfg.DreamingPlan, host.cfg.DreamingRun, host.cfg.RoutingUser
	if _, err := restarted.engineFor("alice"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Tick(context.Background(), now); err != nil || calls != 2 {
		t.Fatalf("restart reran completed night: calls=%d err=%v", calls, err)
	}
}

func TestDreamingDisabledRemovesOnlySystemClock(t *testing.T) {
	h := newHarness(t)
	grantDreaming(t, h, "alice")
	host := h.host(t)
	if _, err := host.engineFor("alice"); err != nil {
		t.Fatal(err)
	}
	enabled := true
	host.cfg.DreamingPlan = func(identity.Envelope) (night.Schedule, error) {
		return night.Schedule{Enabled: enabled, LocalTime: "04:00", Timezone: "UTC", Revision: "rev-1"}, nil
	}
	host.cfg.DreamingRun = func(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error) {
		return "", nil
	}
	now := time.Date(2026, 11, 6, 5, 0, 0, 0, time.UTC)
	if err := host.Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(host.ownerRoot("alice"), "schedules", routines.SystemDreamingID+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	enabled = false
	if err := host.Tick(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled owner retained active system clock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host.ownerRoot("alice"), "system", "dreaming", "2026-11-06.json")); err != nil {
		t.Fatalf("disable erased recoverable day history: %v", err)
	}
}

func TestDreamingRepeatedDSTHourUsesOneLocalDate(t *testing.T) {
	h := newHarness(t)
	grantDreaming(t, h, "alice")
	host := h.host(t)
	host.cfg.DreamingPlan = func(identity.Envelope) (night.Schedule, error) {
		return night.Schedule{Enabled: true, LocalTime: "01:30", Timezone: "America/New_York", Revision: "r1"}, nil
	}
	calls := 0
	host.cfg.DreamingRun = func(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error) {
		calls++
		return "", nil
	}
	if _, err := host.engineFor("alice"); err != nil {
		t.Fatal(err)
	}
	for _, now := range []time.Time{
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
	} {
		if err := host.Tick(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("repeated local hour applied %d times", calls)
	}
}
