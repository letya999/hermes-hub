package identity

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/shared/httpapi"
)

// ChannelBinding is a verified link only; pending/revoked proofs are private
// registry state. OIDC provenance belongs to a link, not every user channel.
type ChannelBinding struct {
	ID          string          `json:"id"`
	PrincipalID string          `json:"principal_id"`
	Channel     ChannelIdentity `json:"channel"`
	Issuer      string          `json:"issuer,omitempty"`
	Subject     string          `json:"subject,omitempty"`
}

// ResolveChannel returns one current verified human/link pair. Consumers use
// this for ingress and delivery so provenance cannot come from a stale cache.
func ResolveChannel(ctx context.Context, client Client, channel ChannelIdentity) (Principal, ChannelBinding, error) {
	if client == nil {
		return Principal{}, ChannelBinding{}, ErrUnavailable
	}
	principal, err := client.Resolve(ctx, channel)
	if err != nil {
		return Principal{}, ChannelBinding{}, err
	}
	if principal.Kind != Human || principal.OwnerID != "" || !validID(principal.ID) {
		return Principal{}, ChannelBinding{}, ErrUnavailable
	}
	links, err := client.Bindings(ctx, principal.ID)
	if err != nil {
		return Principal{}, ChannelBinding{}, err
	}
	links, err = checkedBindings(principal.ID, links)
	if err != nil {
		return Principal{}, ChannelBinding{}, err
	}
	for _, link := range links {
		if link.Channel == channel {
			return principal, link, nil
		}
	}
	return Principal{}, ChannelBinding{}, ErrForbidden
}

func checkedBindings(owner string, values []ChannelBinding) ([]ChannelBinding, error) {
	seen := map[ChannelIdentity]bool{}
	for _, value := range values {
		if !validID(value.ID) || value.PrincipalID != owner || value.Channel.ID == "" || len(value.Channel.ID) > 256 || strings.ContainsAny(value.Channel.ID, "\r\n\t ") || (value.Channel.Channel != Telegram && value.Channel.Channel != Slack) || seen[value.Channel] || (value.Issuer == "") != (value.Subject == "") {
			return nil, ErrUnavailable
		}
		seen[value.Channel] = true
	}
	return httpapi.Copy(values)
}

func (c inProc) Bindings(ctx context.Context, owner string) ([]ChannelBinding, error) {
	if !validID(owner) {
		return nil, ErrInvalid
	}
	values, err := c.server.Bindings(ctx, owner)
	if err != nil {
		return nil, httpapi.Normalize(err)
	}
	return checkedBindings(owner, values)
}

func (c httpClient) Bindings(ctx context.Context, owner string) ([]ChannelBinding, error) {
	if !validID(owner) {
		return nil, ErrInvalid
	}
	var values []ChannelBinding
	if err := c.Do(ctx, http.MethodGet, "/v1/identity/bindings/"+url.PathEscape(owner), nil, &values); err != nil {
		return nil, err
	}
	return checkedBindings(owner, values)
}
