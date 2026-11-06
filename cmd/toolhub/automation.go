package main

import (
	"context"
	"errors"
	"os"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/locale"
	"github.com/letya999/hermes-hub/internal/memory/night"
	"github.com/letya999/hermes-hub/internal/routines"
	"github.com/letya999/hermes-hub/internal/toolhub"
	"github.com/letya999/hermes-hub/internal/toolhub/automation"
)

type automationProjection interface {
	EnrolledPrincipals() []string
	EnrolledAuth(string) (identity.Envelope, error)
	CallReadTool(context.Context, string, string, map[string]any) ([]byte, error)
}

func automationServiceToken() string {
	if token := os.Getenv("HUB_COMMUNICATION_AUTH"); token != "" {
		return token
	}
	return os.Getenv("HUB_RUNTIME_AUTH")
}

// newAutomationHost checks every mounted owner before recovery can write a
// class or the ToolHub listener can accept a control request.
func newAutomationHost(ctx context.Context, projection automationProjection, store *toolhub.Store) (*automation.Host, error) {
	token := automationServiceToken()
	jobURL := os.Getenv("HUB_AUTOMATION_JOB_URL")
	communicationURL := os.Getenv("HUB_COMMUNICATION_CONTROL_URL")
	principals := projection.EnrolledPrincipals()
	if token == "" || jobURL == "" || communicationURL == "" || len(principals) == 0 {
		return nil, errors.New("automation: startup authority unavailable")
	}
	jobPath := os.Getenv("HUB_AUTOMATION_JOB_PATH")
	if jobPath != "" && jobPath != "/v1/execute" || jobPath == "/v1/execute" && len(principals) != 1 {
		return nil, errors.New("automation: unsupported runtime topology")
	}
	client := &automation.HTTPCommunicationClient{URL: communicationURL, Token: token}
	policy := automation.ChannelPolicy{RoutingFile: os.Getenv("HUB_AUTOMATION_ROUTING"), LegacyRoot: os.Getenv("HUB_AUTOMATION_LEGACY_SPOOL")}
	if err := policy.Validate(principals); err != nil {
		return nil, err
	}
	home := func(principal string) commodel.User {
		chatID, _, err := client.OwnerHome(context.Background(), principal)
		if err != nil || chatID == 0 {
			return commodel.User{}
		}
		return commodel.User{ID: principal, Enabled: true, TelegramIDs: []int64{chatID}}
	}
	channels := map[string]routines.OccurrenceChannel{
		"telegram_bot": channel.NewTelegram(channel.TelegramDeps{UserByPrincipal: home}),
		"slack_app":    channel.NewSlack(channel.SlackDeps{}),
		"mock":         channel.NewMock(channel.MockDeps{}),
	}
	var dreamingPlan func(identity.Envelope) (night.Schedule, error)
	var dreamingRun func(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error)
	if nightProjection, ok := projection.(interface {
		NightSchedule(identity.Envelope) (night.Schedule, error)
		NightRun(context.Context, identity.Envelope, string, night.Schedule, locale.Lang, func(context.Context, night.IntentProposal) error) (string, error)
	}); ok {
		dreamingPlan = nightProjection.NightSchedule
		dreamingRun = nightProjection.NightRun
	}
	host, err := automation.NewHost(automation.Config{
		Root:               os.Getenv("HUB_AUTOMATION_ROOT"),
		OwnersDir:          os.Getenv("HUB_AUTOMATION_OWNERS"),
		LegacySpoolDir:     os.Getenv("HUB_AUTOMATION_LEGACY_SPOOL"),
		RequiredPrincipals: principals,
		Environment:        os.Getenv("HUB_AUTOMATION_ENVIRONMENT"),
		Store:              store,
		AuthForPrincipal:   projection.EnrolledAuth,
		Channels:           channels,
		Supervisor:         &automation.HTTPSupervisorClient{URL: jobURL, Token: token, JobPath: jobPath},
		Communication:      client,
		AuthorizeChannel:   policy.Authorize,
		RoutingUser:        policy.User,
		LangOf: func(principal string) locale.Lang {
			_, lang, err := client.OwnerHome(context.Background(), principal)
			if err != nil {
				return locale.Fallback()
			}
			return lang
		},
		ToolCall: func(ctx context.Context, principal string, probe *commodel.WatchProbe) ([]byte, error) {
			if probe == nil {
				return nil, errors.New("automation: tool probe missing")
			}
			return projection.CallReadTool(ctx, principal, probe.Tool, probe.Args)
		},
		DreamingPlan: dreamingPlan,
		DreamingRun:  dreamingRun,
	})
	if err != nil {
		return nil, err
	}
	if err := host.ValidateStorage(); err != nil {
		return nil, err
	}
	if err := host.Recover(ctx); err != nil {
		return nil, err
	}
	return host, nil
}
