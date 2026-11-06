package stack

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/identity"
	"gopkg.in/yaml.v3"
)

type routingUser struct {
	ID            string             `json:"id" yaml:"id"`
	Enabled       bool               `json:"enabled" yaml:"enabled"`
	RuntimeID     string             `json:"runtime_id,omitempty" yaml:"runtime_id"`
	PolicyVersion string             `json:"policy_version,omitempty" yaml:"policy_version"`
	TelegramIDs   []int64            `json:"telegram_ids" yaml:"telegram_ids"`
	SlackIDs      []routingSlackLink `json:"slack_ids,omitempty" yaml:"slack_ids"`
}

type routingSlackLink struct {
	TeamID string `json:"team_id" yaml:"team_id"`
	UserID string `json:"user_id" yaml:"user_id"`
}

// renderAutomationRouting projects only channel identities. ToolHub reads this
// operator-owned file during an outage; provider credentials stay in gateway env.
func renderAutomationRouting(dir string, s Settings, secrets map[string]string) error {
	if s.Environment != "dev" && s.Environment != "prod" {
		return errors.New("invalid automation routing environment")
	}
	var source struct {
		OrganizationID string        `yaml:"organization_id"`
		SpoolDir       string        `yaml:"spool_dir"`
		Users          []routingUser `yaml:"users"`
	}
	if body, err := os.ReadFile(filepath.Join(dir, "communication.users.yaml")); err == nil {
		if err := yaml.Unmarshal(body, &source); err != nil {
			return errors.New("invalid communication routing config")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else {
		source.OrganizationID = s.Organization
		if source.OrganizationID == "" {
			source.OrganizationID = "personal"
		}
		user := routingUser{ID: s.User, Enabled: true, RuntimeID: s.User, PolicyVersion: policyVersion(s)}
		for _, raw := range strings.Split(secrets["TELEGRAM_ALLOWED_USERS"], ",") {
			if raw = strings.TrimSpace(raw); raw != "" {
				id, err := strconv.ParseInt(raw, 10, 64)
				if err != nil || id <= 0 {
					return errors.New("invalid Telegram routing identity")
				}
				user.TelegramIDs = append(user.TelegramIDs, id)
			}
		}
		for _, raw := range strings.Split(secrets["SLACK_ALLOWED_USERS"], ",") {
			if raw = strings.TrimSpace(raw); raw != "" {
				team, sender, ok := strings.Cut(raw, "/")
				if !ok || team == "" || sender == "" || strings.ContainsAny(raw, " \t@") {
					return errors.New("invalid Slack routing identity")
				}
				user.SlackIDs = append(user.SlackIDs, routingSlackLink{team, sender})
			}
		}
		source.Users = []routingUser{user}
	}
	if !identity.ValidID(source.OrganizationID) || len(source.Users) == 0 || source.SpoolDir != "" && source.SpoolDir != "/data" {
		return errors.New("invalid communication routing owners")
	}
	seen, chats, slack := map[string]bool{}, map[int64]bool{}, map[string]bool{}
	users := make([]routingUser, 0, len(source.Users))
	for _, user := range source.Users {
		if !identity.ValidID(user.ID) || seen[user.ID] {
			return errors.New("invalid communication routing owner")
		}
		seen[user.ID] = true
		if !user.Enabled {
			users = append(users, user)
			continue
		}
		for _, chat := range user.TelegramIDs {
			if chat <= 0 || chats[chat] {
				return errors.New("duplicate communication routing identity")
			}
			chats[chat] = true
		}
		for _, link := range user.SlackIDs {
			key := strings.ToLower(link.TeamID + "/" + link.UserID)
			if link.TeamID == "" || link.UserID == "" || strings.ContainsAny(key, " \t@") || slack[key] {
				return errors.New("duplicate communication Slack identity")
			}
			slack[key] = true
		}
		users = append(users, user)
	}
	body, err := json.Marshal(struct {
		OrganizationID string        `json:"organization_id"`
		Users          []routingUser `json:"users"`
	}{source.OrganizationID, users})
	if err != nil {
		return err
	}
	root := filepath.Join(dir, "automation-routing")
	if err := os.MkdirAll(root, 0o750); err != nil {
		return err
	}
	if info, err := os.Lstat(root); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe communication routing directory")
	}
	if err := os.Chmod(root, 0o750); err != nil {
		return err
	}
	path := filepath.Join(root, s.Environment+".json")
	if err := atomic(path, body); err != nil {
		return err
	}
	return os.Chmod(path, 0o640)
}
