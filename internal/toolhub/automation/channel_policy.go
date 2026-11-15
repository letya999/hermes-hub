package automation

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"

	"github.com/letya999/hermes-hub/internal/channel"
	"github.com/letya999/hermes-hub/internal/commodel"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/routines"
)

// ChannelPolicy reads operator routing and the gateway's existing task/link
// bindings directly. It never needs the communication HTTP process to run.
type ChannelPolicy struct {
	RoutingFile string
	LegacyRoot  string
}

func (p ChannelPolicy) users() (string, map[string]commodel.User, map[int64]string, error) {
	if !filepath.IsAbs(p.RoutingFile) || !filepath.IsAbs(p.LegacyRoot) {
		return "", nil, nil, errors.New("automation: routing path unavailable")
	}
	var route struct {
		OrganizationID string          `json:"organization_id"`
		Users          []commodel.User `json:"users"`
	}
	if err := readRoutingJSON(p.RoutingFile, &route); err != nil {
		return "", nil, nil, err
	}
	if !identity.ValidID(route.OrganizationID) || len(route.Users) == 0 {
		return "", nil, nil, errors.New("automation: invalid routing owners")
	}
	users, chats, slack := map[string]commodel.User{}, map[int64]string{}, map[string]string{}
	for _, user := range route.Users {
		if !identity.ValidID(user.ID) || users[user.ID].ID != "" {
			return "", nil, nil, errors.New("automation: duplicate routing owner")
		}
		users[user.ID] = user
		if !user.Enabled {
			continue
		}
		for _, chat := range user.TelegramIDs {
			if chat <= 0 || chats[chat] != "" {
				return "", nil, nil, errors.New("automation: duplicate routing chat")
			}
			chats[chat] = user.ID
		}
		for _, link := range user.SlackIDs {
			key := identity.SlackIdentity(link.TeamID, link.UserID)
			if link.TeamID == "" || link.UserID == "" || slack[key] != "" {
				return "", nil, nil, errors.New("automation: duplicate routing Slack identity")
			}
			slack[key] = user.ID
		}
	}
	return route.OrganizationID, users, chats, nil
}

func (p ChannelPolicy) Validate(principals []string) error {
	_, users, _, err := p.users()
	if err != nil {
		return err
	}
	for _, principal := range principals {
		if users[principal].ID == "" {
			return errors.New("automation: enrolled routing owner unavailable")
		}
	}
	return nil
}

// User returns the operator-authored routing owner, never a caller-provided
// copy. Destination binding uses it to check the live chat and task owner.
func (p ChannelPolicy) User(principal string) (commodel.User, error) {
	_, users, _, err := p.users()
	if err != nil {
		return commodel.User{}, err
	}
	user := users[principal]
	if !user.Enabled || user.ID == "" {
		return commodel.User{}, errUnauthorized
	}
	return user, nil
}

func readRoutingJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return errors.New("automation: routing record unavailable")
	}
	body, err := os.ReadFile(path) // #nosec G703 -- read-only, validated local routing path
	if err != nil || len(body) > 64*1024 || json.Unmarshal(body, target) != nil {
		return errors.New("automation: invalid routing record")
	}
	return nil
}

func (p ChannelPolicy) taskOwned(job commodel.Job) bool {
	if job.TaskID == "" || job.TaskID == commodel.DefaultTaskID {
		return true
	}
	bindings := legacyBindings{root: p.LegacyRoot}
	task, err := bindings.loadTask(job.PrincipalID, job.TaskID)
	if err != nil || task.Archived || task.ContextID != job.ContextID || task.ChatID != job.ChatID || task.TopicID != job.TopicID {
		return false
	}
	if job.TopicID != 0 {
		bound, err := bindings.topicTask(job.PrincipalID, job.ChatID, job.TopicID)
		if err != nil || bound != job.TaskID {
			return false
		}
	}
	return true
}

func (p ChannelPolicy) mockOwner(chatID int64) (string, error) {
	var link struct {
		Principal string `json:"principal"`
	}
	if chatID <= 0 || readRoutingJSON(filepath.Join(p.LegacyRoot, "mock-links", strconv.FormatInt(chatID, 10)+".json"), &link) != nil {
		return "", errors.New("automation: mock link unavailable")
	}
	return link.Principal, nil
}

func (p ChannelPolicy) Authorize(_ context.Context, scheduleID string, job commodel.Job) error {
	org, users, chats, err := p.users()
	if err != nil {
		return err
	}
	user := users[job.PrincipalID]
	if !commodel.IDPattern.MatchString(scheduleID) || !user.Enabled || user.ID == "" || job.UserID != user.ID || job.ActorID != user.ID || job.ScopeID != "user:"+user.ID || job.OrganizationID != org || job.Trigger != "cron" || !p.taskOwned(job) {
		return errors.New("automation: channel owner unavailable")
	}
	canonical := user.Envelope(job.ChatID)
	switch job.Channel {
	case "telegram_bot":
		if chats[job.ChatID] != user.ID {
			return errors.New("automation: Telegram binding changed")
		}
	case "mock":
		canonical = user.MockEnvelope(job.ChatID)
		if owner, err := p.mockOwner(job.ChatID); err != nil || owner != user.ID {
			return errors.New("automation: mock binding changed")
		}
	case "slack_app":
		if err := channel.NewSlack(channel.SlackDeps{}).AuthorizeOccurrence(job, user, true); err != nil {
			return err
		}
		found := false
		for _, link := range user.SlackIDs {
			if identity.SlackIdentity(link.TeamID, link.UserID) == job.ExternalIdentityID {
				canonical = user.SlackEnvelope(link.TeamID, link.UserID)
				found = true
				break
			}
		}
		if !found {
			return errors.New("automation: Slack binding changed")
		}
	default:
		return errors.New("automation: channel unavailable")
	}
	if job.ConversationID != canonical.ConversationID {
		if job.ConversationID != routines.RoutineConversation(scheduleID) && (job.TaskID == "" || job.TaskID == commodel.DefaultTaskID || job.ConversationID != commodel.TaskConversation(job.ChatID, job.TaskID)) {
			return errors.New("automation: conversation binding changed")
		}
		canonical.ConversationID = job.ConversationID
	}
	if !job.Envelope.Equal(canonical) {
		return errors.New("automation: channel envelope changed")
	}
	return nil
}
