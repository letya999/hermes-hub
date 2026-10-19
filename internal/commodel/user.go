// Package commodel owns the hub's channel-neutral domain model: the durable
// records shared by the communication gateway, channel adapters and the
// routines engine. The package is a leaf — it never imports communication,
// toolhub or the ops plane, so every consumer can depend on the records
// without pulling a service in.
package commodel

import (
	"os"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
)

// envOr reads operator config for a model-level default (policy version);
// model records keep their fallback here so adapters can construct the same
// envelope without reaching gateway config.
func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// SlackLink is a verified workspace+sender pair. Email and display name never fill it.
type SlackLink struct {
	TeamID string `yaml:"team_id" json:"team_id"`
	UserID string `yaml:"user_id" json:"user_id"`
}

func (l SlackLink) Key() string {
	return strings.ToLower(strings.TrimSpace(l.TeamID)) + "/" + strings.ToLower(strings.TrimSpace(l.UserID))
}

// User is the stable internal user plus its channel bindings and runtime roots.
type User struct {
	RuntimeID      string            `yaml:"runtime_id,omitempty" json:"runtime_id,omitempty"`
	PolicyVersion  string            `yaml:"policy_version,omitempty" json:"policy_version,omitempty"`
	ID             string            `yaml:"id" json:"id"`
	Enabled        bool              `yaml:"enabled" json:"enabled"`
	TelegramIDs    []int64           `yaml:"telegram_ids" json:"telegram_ids"`
	SlackIDs       []SlackLink       `yaml:"slack_ids,omitempty" json:"slack_ids,omitempty"`
	StateDir       string            `yaml:"state_dir" json:"state_dir"`
	WorkspaceDir   string            `yaml:"workspace_dir" json:"workspace_dir"`
	Features       []string          `yaml:"features,omitempty" json:"features,omitempty"`
	PolicyDisabled []string          `yaml:"policy_disabled,omitempty" json:"policy_disabled,omitempty"`
	ConfiguredEnv  map[string]bool   `yaml:"-" json:"-"`
	Env            map[string]string `yaml:"-" json:"-"`
}

// envelope binds each verified channel user independently of process-wide env.
func (user User) Envelope(externalID int64) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.TelegramEnvelope(user.ID, externalID, runtimeID, policy)
}

func (user User) SlackEnvelope(teamID, slackUser string) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.SlackEnvelope(user.ID, teamID, slackUser, runtimeID, policy)
}

func (user User) MockEnvelope(chatID int64) identity.Envelope {
	runtimeID, policy := user.RuntimeID, user.PolicyVersion
	if runtimeID == "" {
		runtimeID = user.ID
	}
	if policy == "" {
		policy = envOr("HUB_POLICY_VERSION", "policy-1")
	}
	return identity.MockEnvelope(user.ID, chatID, runtimeID, policy)
}
