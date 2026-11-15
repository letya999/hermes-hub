//go:build integration

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/devcheck"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func TestM32GatewayControlPrincipalIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	f, err := devcheck.NewM32Fixture(t.TempDir(), "prod", "http://127.0.0.1:1/v1", "control")
	m32Require(t, err)
	h := newM32Lifecycle(t, ctx, "", "prod", f)
	stop := h.startToolHub(t, ctx)
	t.Cleanup(stop)
	control := automation.ControlClient{URL: h.toolURL, Token: h.control}
	for i, user := range f.Users {
		item := commodel.Schedule{ScheduleID: "m32-control-" + user, Envelope: f.Auth[user], OrganizationID: "personal", UserID: user, ActorID: user, ScopeID: "user:" + user, Channel: "telegram_bot", ChatID: int64(11 + i), Timezone: "UTC", Expression: "0 0 1 1 *", Input: "M32 future control isolation", JobKind: "reminder", Enabled: true}
		m32Require(t, control.Call(ctx, f.Auth[user], "schedule.create", map[string]any{"schedule": item}, nil))
	}
	for i, user := range f.Users {
		r, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Getenv("HUB_COMMUNICATION_CONTROL_URL")+"/v1/routines", nil)
		m32Require(t, err)
		r.Header.Set("Authorization", "Bearer "+h.tokens[user])
		// Both the primary and the enrolled sibling bearer must ignore an
		// attempt to select the other principal through the request header.
		r.Header.Set("X-Hub-Principal", f.Users[1-i])
		response, err := http.DefaultClient.Do(r)
		m32Require(t, err)
		body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		m32Require(t, err)
		m32Require(t, response.Body.Close())
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "m32-control-"+user) || strings.Contains(string(body), "m32-control-"+f.Users[1-i]) {
			t.Fatal("gateway runtime bearer accessed a routine selected by a foreign header")
		}
	}
}
