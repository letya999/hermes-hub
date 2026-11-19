package communication

import (
	"strconv"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

// LegacyChannelIdentity decodes a stored routing ID without treating it as
// proof. Hashed Slack IDs require the exact configured account, never a guess.
func LegacyChannelIdentity(transport string, users []User) (contract.ChannelIdentity, error) {
	if id, ok := strings.CutPrefix(transport, "telegram-"); ok {
		value, err := strconv.ParseInt(id, 10, 64)
		if err == nil && value > 0 && strconv.FormatInt(value, 10) == id {
			return telegramIdentity(value), nil
		}
	}
	for _, user := range users {
		for _, link := range user.SlackIDs {
			if identity.SlackIdentity(link.TeamID, link.UserID) == transport {
				return contract.ChannelIdentity{Channel: contract.Slack, ID: link.Key()}, nil
			}
		}
	}
	if id, ok := strings.CutPrefix(transport, "slack-"); ok {
		team, user, ok := strings.Cut(id, "-")
		if ok && team != "" && user != "" && !strings.ContainsAny(team+user, "/-\r\n\t ") && identity.SlackIdentity(team, user) == transport {
			return contract.ChannelIdentity{Channel: contract.Slack, ID: (SlackLink{TeamID: team, UserID: user}).Key()}, nil
		}
	}
	return contract.ChannelIdentity{}, contract.ErrInvalid
}
