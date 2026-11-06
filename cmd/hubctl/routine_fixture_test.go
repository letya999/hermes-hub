package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

type allowRoutineRights struct{}

func (allowRoutineRights) Decide(identity.Envelope, string, string, string, time.Time) (api.RightDecision, error) {
	return api.RightDecision{Allow: true}, nil
}

// routineHostFixture stands up a real ToolHub automation host on temporary
// storage so routine CLI tests exercise the production control surface.
func routineHostFixture(t *testing.T, principals ...string) (string, func()) {
	t.Helper()
	host, err := automation.NewHost(automation.Config{
		Root: filepath.Join(t.TempDir(), "automation"), Environment: "dev", Store: allowRoutineRights{},
		AuthForPrincipal: func(principal string) (identity.Envelope, error) {
			for _, p := range principals {
				if p == principal {
					return identity.TelegramEnvelope(principal, 1, principal, "policy-1"), nil
				}
			}
			return identity.Envelope{}, errors.New("fixture owner unavailable")
		},
		Channels:         map[string]routines.OccurrenceChannel{"telegram_bot": channel.NewTelegram(channel.TelegramDeps{})},
		Supervisor:       &automation.HTTPSupervisorClient{URL: "http://127.0.0.1", Token: "fixture"},
		Communication:    &automation.HTTPCommunicationClient{URL: "http://127.0.0.1", Token: "fixture"},
		AuthorizeChannel: func(context.Context, string, commodel.Job) error { return nil },
		LangOf:           func(string) locale.Lang { return locale.LangRU },
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(host.ControlHandler("hubctl-test"))
	return server.URL, func() {
		server.Close()
		host.Close()
	}
}
