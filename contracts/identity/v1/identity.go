// Package identity defines the v1 principal and verified-channel read contract.
package identity

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/letya999/hermes-hub/contracts/internal/wire"
	primitive "github.com/letya999/hermes-hub/internal/identity"
)

var (
	ErrNotFound    = wire.ErrNotFound
	ErrConflict    = wire.ErrConflict
	ErrForbidden   = wire.ErrForbidden
	ErrInvalid     = wire.ErrInvalid
	ErrUnavailable = wire.ErrUnavailable
)

type PrincipalKind string

const (
	Human    PrincipalKind = "human"
	Agent    PrincipalKind = "agent"
	System   PrincipalKind = "system"
	Telegram               = "telegram_bot"
	Slack                  = "slack_app"
)

type Principal struct {
	ID             string        `json:"id"`
	Kind           PrincipalKind `json:"kind"`
	OwnerID        string        `json:"owner_id,omitempty"`
	OrganizationID string        `json:"organization_id,omitempty"`
}

// ID is a decimal Telegram ID or the canonical Slack team/user key.
type ChannelIdentity struct {
	Channel string `json:"channel"`
	ID      string `json:"id"`
}

type Client interface {
	Resolve(context.Context, ChannelIdentity) (Principal, error)
	Principal(context.Context, string) (Principal, error)
	Template(context.Context, TemplateRequest) (AgentTemplate, error)
	Bindings(context.Context, string) ([]ChannelBinding, error)
}

func validID(id string) bool { return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\r\n") }

type inProc struct{ server Client }

func NewInProc(server Client) Client { return inProc{server} }

func (c inProc) Resolve(ctx context.Context, channel ChannelIdentity) (Principal, error) {
	if (channel.Channel != Telegram && channel.Channel != Slack) || channel.ID == "" || len(channel.ID) > 256 || strings.ContainsAny(channel.ID, "\r\n") {
		return Principal{}, ErrInvalid
	}
	principal, err := c.server.Resolve(ctx, channel)
	if err != nil {
		return Principal{}, wire.Normalize(err)
	}
	return wire.Copy(principal)
}

func (c inProc) Principal(ctx context.Context, id string) (Principal, error) {
	if !validID(id) {
		return Principal{}, ErrInvalid
	}
	principal, err := c.server.Principal(ctx, id)
	if err != nil {
		return Principal{}, wire.Normalize(err)
	}
	return wire.Copy(principal)
}

func (c inProc) Template(ctx context.Context, query TemplateRequest) (AgentTemplate, error) {
	if !validID(query.Subject) || !validID(query.Actor) || !primitive.ValidID(query.Space) {
		return AgentTemplate{}, ErrInvalid
	}
	value, err := c.server.Template(ctx, query)
	if err != nil {
		return AgentTemplate{}, wire.Normalize(err)
	}
	if !validTemplate(query, value) {
		return AgentTemplate{}, ErrUnavailable
	}
	return wire.Copy(value)
}

func validTemplate(query TemplateRequest, value AgentTemplate) bool {
	if (value.SpaceKind != "private" && value.SpaceKind != "shared" && value.SpaceKind != "organization") || (value.OrganizationID != "" && !primitive.ValidID(value.OrganizationID)) {
		return false
	}
	if value.Subject.ID != query.Subject || value.Subject.Kind != Human || value.Subject.OwnerID != "" || value.Actor.ID != query.Actor || value.Actor.Kind != Agent || value.Actor.OwnerID != query.Subject || value.Actor.OrganizationID != value.Subject.OrganizationID || value.Space != query.Space {
		return false
	}
	switch value.Restrictions.Mode {
	case "read-only", "plan", "ask", "ask-critical", "full-access":
		return true
	}
	return false
}

type httpClient struct{ wire.Client }

func NewHTTPClient(baseURL, token string, hc *http.Client) Client {
	return httpClient{wire.Client{BaseURL: baseURL, Token: token, HTTP: hc}}
}

func (c httpClient) Resolve(ctx context.Context, channel ChannelIdentity) (Principal, error) {
	var principal Principal
	err := c.Do(ctx, http.MethodPost, "/v1/identity/resolve", channel, &principal)
	return principal, err
}

func (c httpClient) Principal(ctx context.Context, id string) (Principal, error) {
	if !validID(id) {
		return Principal{}, ErrInvalid
	}
	var principal Principal
	err := c.Do(ctx, http.MethodGet, "/v1/identity/principals/"+url.PathEscape(id), nil, &principal)
	return principal, err
}

func (c httpClient) Template(ctx context.Context, query TemplateRequest) (AgentTemplate, error) {
	var value AgentTemplate
	err := c.Do(ctx, http.MethodPost, "/v1/identity/template", query, &value)
	if err == nil && !validTemplate(query, value) {
		return AgentTemplate{}, ErrUnavailable
	}
	return value, err
}

func Handler(server Client, token string) http.Handler {
	return wire.Handle(token, func(r *http.Request) (any, error) {
		client := NewInProc(server)
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/identity/resolve":
			var channel ChannelIdentity
			if err := wire.Decode(r, &channel); err != nil {
				return nil, err
			}
			return client.Resolve(r.Context(), channel)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/identity/template":
			var query TemplateRequest
			if err := wire.Decode(r, &query); err != nil {
				return nil, err
			}
			return client.Template(r.Context(), query)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/identity/bindings/"):
			return client.Bindings(r.Context(), strings.TrimPrefix(r.URL.Path, "/v1/identity/bindings/"))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/identity/principals/"):
			return client.Principal(r.Context(), strings.TrimPrefix(r.URL.Path, "/v1/identity/principals/"))
		default:
			return nil, ErrNotFound
		}
	})
}
