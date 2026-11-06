package communication

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

func automationGatewayFixture(t *testing.T, cfg Config) (*Gateway, *automation.Host, string) {
	t.Helper()
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "automation")
	host, err := automation.NewHost(automation.Config{
		Root: root, Environment: "dev", Store: allowAutomationRights{}, LegacySpoolDir: cfg.SpoolDir,
		AuthForPrincipal: func(principal string) (identity.Envelope, error) {
			user := g.user(principal)
			if user.ID != principal || !user.Enabled {
				return identity.Envelope{}, errors.New("fixture owner unavailable")
			}
			var chatID int64
			if len(user.TelegramIDs) != 0 {
				chatID = user.TelegramIDs[0]
			}
			return user.Envelope(chatID), nil
		},
		Channels: map[string]routines.OccurrenceChannel{
			"telegram_bot": channel.NewTelegram(channel.TelegramDeps{}),
			"mock":         channel.NewMock(channel.MockDeps{}),
			"slack_app":    channel.NewSlack(channel.SlackDeps{}),
		},
		Supervisor:       &automation.HTTPSupervisorClient{URL: "http://127.0.0.1", Token: "fixture"},
		Communication:    &automation.HTTPCommunicationClient{URL: "http://127.0.0.1", Token: "fixture"},
		AuthorizeChannel: func(context.Context, string, commodel.Job) error { return nil },
		RoutingUser: func(principal string) (commodel.User, error) {
			user := g.user(principal)
			if user.ID != principal || !user.Enabled {
				return commodel.User{}, errors.New("fixture owner unavailable")
			}
			return user, nil
		},
		LangOf: func(string) locale.Lang { return locale.LangRU },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	server := httptest.NewServer(host.ControlHandler("automation-test"))
	t.Cleanup(server.Close)
	g.config.ToolHubURL, g.config.AutomationAuth = server.URL, "automation-test"
	return g, host, root
}
