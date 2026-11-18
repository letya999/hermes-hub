package automation

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/letya999/hermes-hub/internal/commodel"
)

func TestAutomationControlPausesAllPrincipalSchedules(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.envelope(t, "alice"), h.envelope(t, "bob")
	h.grant(t, "alice", "write")
	h.grant(t, "bob", "write")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	ctx := context.Background()
	for _, item := range []commodel.Schedule{draft("rt-alice-1", alice), draft("rt-alice-2", alice), draft("rt-bob", bob)} {
		if err := client.Call(ctx, item.Envelope, "schedule.create", map[string]any{"schedule": item}, &item); err != nil {
			t.Fatal(err)
		}
	}
	var paused struct {
		Schedules int `json:"schedules"`
	}
	if err := client.Call(ctx, alice, "schedule.pause", map[string]string{"id": "*"}, &paused); err != nil || paused.Schedules != 2 {
		t.Fatalf("pause all: %+v, %v", paused, err)
	}
	var list []commodel.Schedule
	if err := client.Call(ctx, alice, "schedule.list", nil, &list); err != nil || len(list) != 2 {
		t.Fatalf("alice list: %+v, %v", list, err)
	}
	for _, item := range list {
		if !item.Paused {
			t.Fatalf("alice schedule not paused: %+v", item)
		}
	}
	list = nil
	if err := client.Call(ctx, bob, "schedule.list", nil, &list); err != nil || len(list) != 1 || list[0].Paused {
		t.Fatalf("bob list: %+v, %v", list, err)
	}
	paused.Schedules = -1
	if err := client.Call(ctx, alice, "schedule.pause", map[string]string{"id": "*"}, &paused); err != nil || paused.Schedules != 0 {
		t.Fatalf("second pause all: %+v, %v", paused, err)
	}
}

func TestAutomationControlPauseAllSkipsNeverOnboardedPrincipal(t *testing.T) {
	h := newHarness(t)
	ghost := h.envelope(t, "ghost")
	h.grant(t, "ghost", "write")
	host := h.host(t)
	server := httptest.NewServer(host.ControlHandler("control"))
	defer server.Close()
	client := ControlClient{URL: server.URL, Token: "control"}
	var paused struct {
		Schedules int `json:"schedules"`
	}
	// A principal whose owner subtree never materialized owns no schedules;
	// revocation must not wedge the coordinator on a user that never ran
	// automation.
	if err := client.Call(context.Background(), ghost, "schedule.pause", map[string]string{"id": "*"}, &paused); err != nil || paused.Schedules != 0 {
		t.Fatalf("pause all for never-onboarded principal: %+v, %v", paused, err)
	}
}
