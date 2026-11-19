package communication

import (
	"context"
	"strconv"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identity"
)

// Principal exposes only the enabled legacy principal, never its runtime config.
func (g *Gateway) Principal(_ context.Context, id string) (contract.Principal, error) {
	user := g.user(id)
	if !user.Enabled {
		return contract.Principal{}, contract.ErrNotFound
	}
	return contract.Principal{ID: user.ID, Kind: contract.Human}, nil
}

func (g *Gateway) Resolve(ctx context.Context, channel contract.ChannelIdentity) (contract.Principal, error) {
	principal, transportID := "", ""
	switch channel.Channel {
	case contract.Telegram:
		id, err := strconv.ParseInt(channel.ID, 10, 64)
		if err != nil || strconv.FormatInt(id, 10) != channel.ID {
			return contract.Principal{}, contract.ErrInvalid
		}
		transportID = "telegram-" + channel.ID
		for _, user := range g.config.Users {
			for _, candidate := range user.TelegramIDs {
				if candidate != id {
					continue
				}
				if principal != "" && principal != user.ID {
					return contract.Principal{}, contract.ErrConflict
				}
				principal = user.ID
			}
		}
	case contract.Slack:
		team, userID, ok := strings.Cut(channel.ID, "/")
		link := SlackLink{TeamID: team, UserID: userID}
		if !ok || team == "" || userID == "" || link.Key() != channel.ID || strings.Contains(userID, "/") {
			return contract.Principal{}, contract.ErrInvalid
		}
		transportID = identity.SlackIdentity(team, userID)
		for _, user := range g.config.Users {
			for _, candidate := range user.SlackIDs {
				if candidate.Key() != channel.ID {
					continue
				}
				if principal != "" && principal != user.ID {
					return contract.Principal{}, contract.ErrConflict
				}
				principal = user.ID
			}
		}
	default:
		return contract.Principal{}, contract.ErrInvalid
	}
	// Static assignments retain precedence; verified SSO links are the fallback.
	if principal == "" {
		if user, ok := g.iamResolveTransport(transportID); ok {
			principal = user.ID
		}
	}
	return g.Principal(ctx, principal)
}
