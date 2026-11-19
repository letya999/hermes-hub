package communication

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/identityhub"
)

// Historical gateway fixtures provision a real temporary registry explicitly;
// production New never imports config or falls back to its channel lists.
func newTestGateway(t testing.TB, config Config) (*Gateway, error) {
	t.Helper()
	if config.Identity == nil && config.Validate() == nil {
		provisionTestIdentity(t, &config)
	}
	return New(config)
}

func provisionTestIdentity(t testing.TB, config *Config) {
	t.Helper()
	if config.Identity != nil {
		return
	}
	r, err := identityhub.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	org := config.OrganizationID
	if org == "" {
		org = "personal"
	}
	input := identityhub.LegacyConfiguration{Name: "fixture", Organization: org, Issuer: config.OIDCIssuer, Users: config.Users}
	for _, group := range config.Groups {
		if group.Mode != "topics" {
			input.SharedSpaces = append(input.SharedSpaces, identityhub.LegacySharedSpace{ID: group.Space, Members: group.Members})
		}
	}
	if _, err := r.ImportLegacyConfiguration(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	config.Identity = r
	if config.IdentityLinks == nil {
		config.IdentityLinks = r
	}
}

func fixtureRevokeChannel(t *testing.T, g *Gateway, external int64) {
	t.Helper()
	p, err := g.config.Identity.Resolve(context.Background(), telegramIdentity(external))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.config.IdentityLinks.Unlink(context.Background(), contract.UnlinkRequest{Channel: telegramIdentity(external), PrincipalID: p.ID}); err != nil {
		t.Fatal(err)
	}
}

func fixtureLinkChannel(t *testing.T, g *Gateway, owner string, channel contract.ChannelIdentity) {
	t.Helper()
	r := g.config.Identity.(*identityhub.Registry)
	if p, err := r.Resolve(context.Background(), channel); err == nil && p.ID == owner {
		return
	}
	challenge, err := r.BeginLink(context.Background(), owner, channel, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyLink(context.Background(), challenge.ID, challenge.Proof, channel); err != nil {
		t.Fatal(err)
	}
}

func fixtureLinkSlack(t *testing.T, g *Gateway, owner, team, user string) {
	t.Helper()
	fixtureLinkChannel(t, g, owner, contract.ChannelIdentity{Channel: contract.Slack, ID: (SlackLink{TeamID: team, UserID: user}).Key()})
}

func fixtureSetSharedMembers(t *testing.T, g *Gateway, space string, members ...int64) {
	t.Helper()
	r := g.config.Identity.(*identityhub.Registry)
	if _, err := r.ImportLegacy(context.Background(), "fixture-operator", "personal", []User{{ID: "test-operator", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := r.BootstrapPlatformAdmin(context.Background(), "test-operator"); err != nil {
		t.Fatal(err)
	}
	var principals []string
	for _, external := range members {
		p, err := r.Resolve(context.Background(), telegramIdentity(external))
		if err != nil {
			t.Fatal(err)
		}
		principals = append(principals, p.ID)
	}
	if err := r.SetSharedMembers(context.Background(), "test-operator", space, principals); err != nil {
		t.Fatal(err)
	}
}

func fixtureOIDCLink(t *testing.T, g *Gateway, principal, transport, subject string) error {
	t.Helper()
	r, ok := g.config.Identity.(*identityhub.Registry)
	if !ok {
		return fmt.Errorf("fixture requires registry")
	}
	channel, err := LegacyChannelIdentity(transport, g.config.Users)
	if err != nil {
		return err
	}
	_, err = r.ImportLegacyConfiguration(context.Background(), identityhub.LegacyConfiguration{Name: transport, Organization: g.config.OrganizationID, Issuer: g.config.OIDCIssuer, VerifiedLinks: []contract.OIDCLink{{Channel: channel, PrincipalID: principal, Issuer: g.config.OIDCIssuer, Subject: subject}}})
	return err
}

func fixtureBinding(g *Gateway, principal, transport string) (contract.ChannelBinding, bool) {
	channel, err := LegacyChannelIdentity(transport, g.config.Users)
	if err != nil {
		return contract.ChannelBinding{}, false
	}
	bindings, err := g.config.Identity.Bindings(context.Background(), principal)
	if err == nil {
		for _, binding := range bindings {
			if binding.Channel == channel {
				return binding, true
			}
		}
	}
	return contract.ChannelBinding{}, false
}
