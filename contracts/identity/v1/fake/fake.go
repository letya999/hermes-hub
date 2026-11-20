// Package fake is a foreign identity-hub fixture, not a service
// implementation. It passes the same contract conformance suite as the
// real owner.
package fake

import (
	"context"

	identity "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Server contains immutable records supplied by the test. It is safe for
// concurrent reads; tests construct a new Server rather than mutating it.
type Server struct {
	Principals map[string]identity.Principal
	Links      []identity.ChannelBinding
	Keys       []identity.PublicSigningKey
	Policies   map[string]identity.SignedPolicy
}

func (s Server) Resolve(ctx context.Context, channel identity.ChannelIdentity) (identity.Principal, error) {
	if err := ctx.Err(); err != nil {
		return identity.Principal{}, err
	}
	for _, link := range s.Links {
		if link.Channel == channel {
			return s.principal(link.PrincipalID)
		}
	}
	return identity.Principal{}, identity.ErrNotFound
}

func (s Server) Principal(ctx context.Context, id string) (identity.Principal, error) {
	if err := ctx.Err(); err != nil {
		return identity.Principal{}, err
	}
	return s.principal(id)
}

func (s Server) principal(id string) (identity.Principal, error) {
	if p, ok := s.Principals[id]; ok {
		return httpapi.Copy(p)
	}
	return identity.Principal{}, identity.ErrNotFound
}

func (s Server) Bindings(ctx context.Context, owner string) ([]identity.ChannelBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := s.Principals[owner]; !ok {
		return nil, identity.ErrNotFound
	}
	var links []identity.ChannelBinding
	for _, link := range s.Links {
		if link.PrincipalID == owner {
			links = append(links, link)
		}
	}
	return httpapi.Copy(links)
}

// Template derives the default private-space template: the subject must be a
// known principal, the actor its own default agent and the space its private
// space, matching the real owner's personal-agent derivation.
func (s Server) Template(ctx context.Context, query identity.TemplateRequest) (identity.AgentTemplate, error) {
	if err := ctx.Err(); err != nil {
		return identity.AgentTemplate{}, err
	}
	subject, ok := s.Principals[query.Subject]
	if !ok || query.Actor != "agent:"+query.Subject+":default" || query.Space != query.Subject {
		return identity.AgentTemplate{}, identity.ErrForbidden
	}
	return httpapi.Copy(identity.AgentTemplate{
		SpaceKind:      "private",
		OrganizationID: subject.OrganizationID,
		Subject:        subject,
		Actor:          identity.Principal{ID: query.Actor, Kind: identity.Agent, OwnerID: subject.ID, OrganizationID: subject.OrganizationID},
		Space:          query.Space,
		Restrictions:   identity.Restrictions{Mode: "ask-critical"},
	})
}

func (s Server) SigningKeys(ctx context.Context) ([]identity.PublicSigningKey, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return httpapi.Copy(s.Keys)
}

func (s Server) Policy(ctx context.Context, scope string) (identity.SignedPolicy, error) {
	if err := ctx.Err(); err != nil {
		return identity.SignedPolicy{}, err
	}
	if p, ok := s.Policies[scope]; ok {
		return httpapi.Copy(p)
	}
	return identity.SignedPolicy{}, identity.ErrNotFound
}
