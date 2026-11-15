package communication

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

// principalHash mirrors the owner's safeName layout so migrated tests can
// stage durable records at the exact path the production host reads.
func principalHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func automationGatewayFixture(t *testing.T, cfg Config) (*Gateway, *automation.Host, string) {
	t.Helper()
	return automationGatewayFixtureChannels(t, cfg, nil)
}

// automationGatewayFixtureChannels is automationGatewayFixture with extra
// provider channels registered on the ToolHub host — capability assertions
// register their recording doubles here, not on the gateway.
func automationGatewayFixtureChannels(t *testing.T, cfg Config, extra map[string]routines.OccurrenceChannel) (*Gateway, *automation.Host, string) {
	t.Helper()
	return automationGatewayFixturePorts(t, cfg, extra, nil)
}

// automationGatewayFixturePorts additionally lets a test swap the host's
// supervisor client — script/command probe tests point it at a recording
// server implementing the runtime contract.
func automationGatewayFixturePorts(t *testing.T, cfg Config, extra map[string]routines.OccurrenceChannel, supervisor automation.SupervisorClient) (*Gateway, *automation.Host, string) {
	t.Helper()
	return automationGatewayFixtureProbe(t, cfg, extra, supervisor, nil)
}

// automationGatewayFixtureProbe additionally injects the host's ToolCall —
// the mcp_tool acquisition seam — so event/stream tests can drive real
// polls through httptest connectors.
func automationGatewayFixtureProbe(t *testing.T, cfg Config, extra map[string]routines.OccurrenceChannel, supervisor automation.SupervisorClient, toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error)) (*Gateway, *automation.Host, string) {
	t.Helper()
	return automationGatewayFixtureIO(t, cfg, extra, supervisor, toolCall, nil)
}

// automationGatewayFixtureIO additionally injects the host's Fetch — the http
// probe I/O seam — so baseline/poll tests drive probes through httptest
// servers instead of the public-egress client.
func automationGatewayFixtureIO(t *testing.T, cfg Config, extra map[string]routines.OccurrenceChannel, supervisor automation.SupervisorClient, toolCall func(context.Context, string, *commodel.WatchProbe) ([]byte, error), fetch func(context.Context, string) ([]byte, error)) (*Gateway, *automation.Host, string) {
	t.Helper()
	g, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "automation")
	channels := map[string]routines.OccurrenceChannel{
		"telegram_bot": channel.NewTelegram(g.telegramDeps()),
		"mock":         channel.NewMock(g.mockDeps()),
		"slack_app":    channel.NewSlack(g.slackDeps()),
	}
	for id, ch := range extra {
		channels[id] = ch
	}
	if supervisor == nil {
		supervisor = &automation.HTTPSupervisorClient{URL: "http://127.0.0.1", Token: "fixture", JobPath: "/v1/execute"}
	}
	// Production wires ChannelPolicy over the operator routing file and the
	// communication task/topic bindings; the fixture authors the same record
	// so a stale binding fences work instead of being waved through.
	routingPath := filepath.Join(t.TempDir(), "routing.json")
	if body, err := json.Marshal(map[string]any{"organization_id": cfg.OrganizationID, "users": cfg.Users}); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(routingPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	policy := automation.ChannelPolicy{RoutingFile: routingPath, LegacyRoot: cfg.SpoolDir}
	// The owner posts deliveries/results back through the gateway's real
	// service intake — the same path the production host takes.
	commServer := httptest.NewServer(g.Handler())
	t.Cleanup(commServer.Close)
	if g.config.ControlAuth == "" {
		g.config.ControlAuth = "control-token"
	}
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
		Channels:         channels,
		Supervisor:       supervisor,
		ToolCall:         toolCall,
		Fetch:            fetch,
		Communication:    &automation.HTTPCommunicationClient{URL: commServer.URL, Token: "automation-test"},
		AuthorizeChannel: policy.Authorize,
		RoutingUser: func(principal string) (commodel.User, error) {
			user := g.user(principal)
			if user.ID != principal || !user.Enabled {
				return commodel.User{}, errors.New("fixture owner unavailable")
			}
			return user, nil
		},
		LangOf: func(principal string) locale.Lang {
			return locale.LangOf(g.user(principal).WorkspaceDir)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Close() })
	// One ToolHub URL, two surfaces the gateway touches: the automation
	// control RPC plus the rights PEP the delivery worker consults before
	// sending owner-principal records.
	control := http.NewServeMux()
	control.Handle("/", host.ControlHandler("automation-test"))
	control.HandleFunc("/v1/rights/decide", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"allow": true})
	})
	server := httptest.NewServer(control)
	t.Cleanup(server.Close)
	t.Setenv("HUB_SUPERVISOR_AUTH", "fixture-supervisor")
	g.config.ToolHubURL, g.config.AutomationAuth = server.URL, "automation-test"
	// Loopback httptest probes must bypass the production egress guard the
	// default fetchWatch client carries.
	g.watchHTTP = &http.Client{Timeout: 10 * time.Second}
	return g, host, root
}
