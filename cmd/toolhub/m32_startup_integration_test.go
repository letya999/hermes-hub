//go:build integration

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/devcheck"
	memorysearch "github.com/letya999/hermes-hub/internal/memory/search"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

// The same authored bootstrap and production service startup as the Docker leg,
// with no execution due: this catches harness drift before a cold container start.
func TestM32ToolHubStartupAndRecovery(t *testing.T) {
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "startup")
	m32Require(t, err)
	h := newM32Lifecycle(t, t.Context(), "", "prod", f)
	stop := h.startToolHub(t, t.Context())
	t.Cleanup(func() { stop() })
	client := automation.ControlClient{URL: h.toolURL, Token: h.control}
	items := make([]commodel.Schedule, len(f.Users))
	assignments := make([]commodel.Assignment, len(f.Users))
	for i, user := range f.Users {
		index, _, err := memorysearch.NewIndexer(filepath.Join(f.Root, "vaults", user)).Ensure()
		m32Require(t, err)
		hits := index.Search("M32-private-marker-"+user, memorysearch.Options{Audience: "private", Limit: 10, Now: time.Now()})
		if len(hits) != 1 || !strings.Contains(hits[0].Doc.Text, "M32-private-marker-"+user) {
			t.Fatal("container acceptance memory seed is not searchable")
		}
		auth := f.Auth[user]
		item := commodel.Schedule{ScheduleID: "m32-future-" + user, Envelope: auth, OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "once:" + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339), Input: "M32 isolated future record", JobKind: "agent", Enabled: true}
		m32Require(t, client.Call(t.Context(), auth, "schedule.create", map[string]any{"schedule": item}, &items[i]))
		if items[i].Revision == 0 || items[i].ScheduleID != item.ScheduleID {
			t.Fatal("created schedule receipt lost its revision or ID")
		}
		if err := client.Call(t.Context(), auth, "schedule.pause", map[string]any{"id": item.ScheduleID, "expected_revision": items[i].Revision + 1}, nil); err == nil {
			t.Fatal("stale schedule mutation accepted")
		}
		m32Require(t, client.Call(t.Context(), auth, "schedule.pause", map[string]any{"id": item.ScheduleID, "expected_revision": items[i].Revision}, nil))
		assignment := commodel.Assignment{AssignmentID: "m32-draft-" + user, Envelope: auth, Intent: "M32 isolated draft", Title: "M32 draft", State: "draft"}
		m32Require(t, client.Call(t.Context(), auth, "assignment.create", map[string]any{"assignment": assignment}, &assignments[i]))
	}
	stop()
	stop = h.startToolHub(t, t.Context())
	for i, user := range f.Users {
		auth := f.Auth[user]
		var item commodel.Schedule
		m32Require(t, client.Call(t.Context(), auth, "schedule.load", map[string]any{"id": items[i].ScheduleID}, &item))
		if !item.Paused || item.Revision <= items[i].Revision {
			t.Fatal("restart lost the paused schedule revision")
		}
		var assignment commodel.Assignment
		m32Require(t, client.Call(t.Context(), auth, "assignment.load", map[string]any{"id": assignments[i].AssignmentID}, &assignment))
		if assignment.State != "draft" || assignment.Revision != assignments[i].Revision {
			t.Fatal("restart changed the draft assignment")
		}
		other := 1 - i
		if err := client.Call(t.Context(), auth, "schedule.load", map[string]any{"id": items[other].ScheduleID}, &item); err == nil {
			t.Fatal("foreign schedule readable")
		}
		if err := client.Call(t.Context(), auth, "assignment.load", map[string]any{"id": assignments[other].AssignmentID}, &assignment); err == nil {
			t.Fatal("foreign assignment readable")
		}
	}
}
