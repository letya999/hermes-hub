package toolhub

import (
	"context"
	"errors"
	"fmt"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type preparedOAuthFlow struct {
	onboardingID string
	url          string
	expires      time.Time
	broker       *oauth.Broker
	// remote marks a generic remote-mcp flow minted from discovered PRM/AS
	// metadata rather than a reviewed prepared-provider contract.
	remote bool
}

// oauthFlowStore serializes pending handoffs behind their own lock; flow
// state never shares the admission mutex or Store's lock.
type oauthFlowStore struct {
	mu    sync.Mutex
	flows map[string]*preparedOAuthFlow
}

// ensurePreparedOAuth starts only a reviewed OAuth handoff. Tool output is
// never interpreted as an authorization URL or a provider configuration.
// resend=true rotates the armed flow: the user asked for the link again, so
// the gateway gets a link-resent event on top of the standing-message edit.
func (c *ControlPlane) ensurePreparedOAuth(ctx context.Context, auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition, resend bool) (map[string]any, error) {
	entry, matched, err := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA})
	if a := definition.OAuth; a != nil {
		entry.StateTarget = a.StateTarget
		entry.OAuth = a
		matched, err = true, nil
	}
	if err != nil || !matched || entry.OAuth == nil {
		if definition.Transport == api.RemoteMCP {
			return c.ensureRemoteOAuth(auth, onboarding, definition)
		}
		return nil, api.ErrUnauthorized
	}
	if c.Injector == nil || c.Secrets == nil {
		return nil, api.ErrUnauthorized
	}
	c.oauthFlows.mu.Lock()
	// ponytail: one lock serializes handoff creation; use per-onboarding locks if starts become a bottleneck.
	defer c.oauthFlows.mu.Unlock()
	for state, flow := range c.oauthFlows.flows {
		if !c.now().Before(flow.expires) {
			delete(c.oauthFlows.flows, state)
			continue
		}
		if flow.onboardingID == onboarding.OnboardingID && !resend {
			onboarding.Phase = api.PhaseAwaitingOAuth
			onboarding.ProviderAuthorizationURL = flow.url
			// The wizard link must outlive the flow it fronts: a status poll
			// hitting a live flow renews the TTL instead of stranding the URL.
			dirty := !c.now().Before(onboarding.WizardExpires)
			if dirty {
				onboarding.WizardExpires = c.now().Add(c.ttl())
			}
			if onboarding.Error != "" || onboarding.ErrorCode != "" {
				onboarding.Error, onboarding.ErrorCode = "", ""
				dirty = true
			}
			if dirty {
				onboarding.Revision++
				if err := c.Store.PutOnboarding(onboarding); err != nil {
					return nil, err
				}
			}
			return c.statusBody(onboarding, false), nil
		}
	}
	effective, err := c.preparedOAuthEffective(auth, definition)
	if err != nil {
		return nil, err
	}
	injection, err := c.Injector(ctx, effective)
	if err != nil {
		return nil, err
	}
	if injection.Cleanup != nil {
		defer injection.Cleanup()
	}
	clientPath := injection.Environment[entry.OAuth.ClientInput]
	var stateDir string
	var clientFile string
	for _, mount := range injection.Mounts {
		if mount.Target == clientPath && mount.ReadOnly {
			clientFile = mount.Source
		}
		if mount.Target == entry.StateTarget && !mount.ReadOnly {
			stateDir = mount.Source
		}
	}
	if clientFile == "" || stateDir == "" {
		return nil, api.ErrIsolation
	}
	if _, err := os.Stat(filepath.Join(stateDir, entry.OAuth.TokenFile)); err == nil {
		return nil, fmt.Errorf("%w: existing token state requires diagnosis", api.ErrUnauthorized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data, err := os.ReadFile(clientFile)
	if err != nil || len(data) > 65536 {
		return nil, api.ErrIsolation
	}
	defer clear(data)
	redirect := c.origin() + "/oauth/callback"
	id, secret, err := entry.OAuth.OAuthClient(data, redirect)
	if err != nil {
		return nil, err
	}
	broker := oauth.NewBroker(c.Secrets, []string{redirect})
	broker.Providers = map[string]oauth.Provider{"recipe": {Name: "recipe", AuthorizeURL: entry.OAuth.AuthorizeURL, TokenURL: entry.OAuth.TokenURL}}
	broker.Clients["recipe"] = oauth.ClientCredential{ID: id, Secret: secret}
	start, err := broker.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: "recipe", Redirect: redirect, Scopes: entry.OAuth.Scopes, AccessType: entry.OAuth.AccessType, Prompt: entry.OAuth.Prompt})
	if err != nil {
		return nil, err
	}
	if !validPreparedOAuthURL(start.AuthorizeURL) {
		return nil, api.ErrInvalid
	}
	if c.oauthFlows.flows == nil {
		c.oauthFlows.flows = make(map[string]*preparedOAuthFlow)
	}
	c.oauthFlows.flows[start.State] = &preparedOAuthFlow{onboardingID: onboarding.OnboardingID, url: start.AuthorizeURL, expires: c.now().Add(c.ttl()), broker: broker}
	onboarding.Phase = api.PhaseAwaitingOAuth
	onboarding.ProviderAuthorizationURL = start.AuthorizeURL
	c.armWizard(&onboarding)
	onboarding.Error, onboarding.ErrorCode = "", ""
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		delete(c.oauthFlows.flows, start.State)
		return nil, err
	}
	api.PostPrepareNotice(ctx, auth, onboarding, "", 0)
	if resend {
		api.DeliverPrepareEvent(ctx, auth, onboarding, "link-resent")
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) preparedOAuthEffective(auth identity.Envelope, definition api.ToolDefinition) (api.EffectiveBinding, error) {
	c.Store.mu.RLock()
	matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
	c.Store.mu.RUnlock()
	if len(matches) != 1 || matches[0].credential.BrokerGrantID == "" {
		return api.EffectiveBinding{}, api.ErrUnauthorized
	}
	connection, credential := matches[0].connection, matches[0].credential
	binding := api.ToolBinding{Schema: api.SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: api.ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = api.DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	ownerID := auth.ContextID + ":" + auth.PrincipalID + ":" + connection.ConnectionID
	return api.EffectiveBinding{Binding: binding, Definition: definition, Connection: &connection, Credential: &credential, WorkloadID: api.WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")}, nil
}

// preparedOAuthOnboardingID peeks at the live flow map so the callback page
// can route the browser back onto the stable wizard link after completion —
// completePreparedOAuth consumes the state, so the lookup happens first.
func (c *ControlPlane) preparedOAuthOnboardingID(state string) string {
	c.oauthFlows.mu.Lock()
	defer c.oauthFlows.mu.Unlock()
	if flow := c.oauthFlows.flows[state]; flow != nil {
		return flow.onboardingID
	}
	return ""
}

func (c *ControlPlane) completePreparedOAuth(ctx context.Context, state, code string) error {
	c.oauthFlows.mu.Lock()
	flow := c.oauthFlows.flows[state]
	delete(c.oauthFlows.flows, state)
	c.oauthFlows.mu.Unlock()
	if flow == nil || !c.now().Before(flow.expires) || code == "" {
		return api.ErrUnauthorized
	}
	onboarding, err := c.Store.onboarding(flow.onboardingID)
	if err != nil || onboarding.Phase != api.PhaseAwaitingOAuth {
		return api.ErrUnauthorized
	}
	if flow.remote {
		return c.completeRemoteOAuth(ctx, flow, onboarding, state, code)
	}
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	meta, err := flow.broker.HandleCallback(auth.PrincipalID, auth.ContextID, onboarding.OnboardingID, state, code, c.origin()+"/oauth/callback")
	if err != nil {
		return err
	}
	values, err := c.Secrets.Get(meta.Locator, auth.PrincipalID)
	if err != nil {
		return err
	}
	defer func() {
		for key := range values {
			values[key] = ""
		}
	}()
	if err := c.Secrets.Delete(meta.Locator, auth.PrincipalID); err != nil {
		return err
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return err
	}
	entry, matched, err := api.PreparedForSource(api.ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA})
	if a := definition.OAuth; a != nil {
		entry.StateTarget = a.StateTarget
		entry.OAuth = a
		matched, err = true, nil
	}
	if err != nil || !matched || entry.OAuth == nil {
		return api.ErrUnauthorized
	}
	effective, err := c.preparedOAuthEffective(auth, definition)
	if err != nil {
		return err
	}
	injection, err := c.Injector(ctx, effective)
	if err != nil {
		return err
	}
	if injection.Cleanup != nil {
		defer injection.Cleanup()
	}
	var stateDir string
	for _, mount := range injection.Mounts {
		if mount.Target == entry.StateTarget && !mount.ReadOnly {
			stateDir = mount.Source
		}
	}
	if stateDir == "" || values["ACCESS_TOKEN"] == "" || values["REFRESH_TOKEN"] == "" || injection.Checkpoint == nil {
		return api.ErrIsolation
	}
	client := flow.broker.Clients["recipe"]
	encoded, err := entry.OAuth.RenderToken(values, client.ID, client.Secret, c.now())
	if err != nil {
		return err
	}
	defer clear(encoded)
	path := filepath.Join(stateDir, entry.OAuth.TokenFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(encoded)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	if err := injection.Checkpoint(); err != nil {
		return err
	}
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ProviderAuthorizationURL = ""
	onboarding.ConfirmationNonce = ""
	onboarding.ConfirmationExpires = time.Time{}
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	if err := c.refreshConfirmation(ctx, auth, &onboarding); err != nil {
		return err
	}
	_, err = c.confirm(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": onboarding.ConfirmationNonce})
	if err != nil {
		return err
	}
	_, err = c.enable(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID})
	return err
}

func validPreparedOAuthURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == "" && !strings.ContainsAny(raw, "\r\n")
}
