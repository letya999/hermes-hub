package communication

import (
	"context"
	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"strconv"
	"strings"
)

func (g *Gateway) identityUser(ctx context.Context, channel contract.ChannelIdentity) (User, bool) {
	principal, link, ok := g.identityChannel(ctx, channel)
	if !ok {
		return User{}, false
	}
	org := g.config.OrganizationID
	if org == "personal" {
		org = ""
	}
	if principal.OrganizationID != org {
		return User{}, false
	}
	user := g.user(principal.ID)
	user.SSOIssuer, user.SSOSubject = link.Issuer, link.Subject
	return user, user.Enabled
}

func (g *Gateway) identityChannel(ctx context.Context, channel contract.ChannelIdentity) (contract.Principal, contract.ChannelBinding, bool) {
	if g == nil || g.config.Identity == nil {
		return contract.Principal{}, contract.ChannelBinding{}, false
	}
	principal, link, err := contract.ResolveChannel(ctx, g.config.Identity, channel)
	return principal, link, err == nil
}

func telegramIdentity(id int64) contract.ChannelIdentity {
	return contract.ChannelIdentity{Channel: contract.Telegram, ID: strconv.FormatInt(id, 10)}
}

func (g *Gateway) projectIdentityChannels(user User) User {
	user.TelegramIDs = nil
	user.SlackIDs = nil
	if !user.Enabled {
		return user
	}
	for _, group := range g.config.Groups {
		if group.Mode != "topics" && group.Space == user.ID {
			return user
		}
	}
	if g.config.Identity == nil {
		user.Enabled = false
		return user
	}
	principal, err := g.config.Identity.Principal(context.Background(), user.ID)
	org := g.config.OrganizationID
	if org == "personal" {
		org = ""
	}
	if err != nil || principal.ID != user.ID || principal.Kind != contract.Human || principal.OwnerID != "" || principal.OrganizationID != org {
		user.Enabled = false
		return user
	}
	bindings, err := g.config.Identity.Bindings(context.Background(), user.ID)
	if err != nil {
		user.Enabled = false
		return user
	}
	for _, binding := range bindings {
		if binding.PrincipalID != user.ID {
			user.Enabled = false
			user.TelegramIDs = nil
			user.SlackIDs = nil
			return user
		}
		switch binding.Channel.Channel {
		case contract.Telegram:
			id, err := strconv.ParseInt(binding.Channel.ID, 10, 64)
			if err == nil && id > 0 {
				user.TelegramIDs = append(user.TelegramIDs, id)
			}
		case contract.Slack:
			team, sender, ok := strings.Cut(binding.Channel.ID, "/")
			if ok {
				user.SlackIDs = append(user.SlackIDs, SlackLink{TeamID: team, UserID: sender})
			}
		}
	}
	return user
}
