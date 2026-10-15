package toolhub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// remoteOAuthProvider names the broker slot for one endpoint's discovered
// provider: per-definition, so two remote servers can never overwrite each
// other's endpoints or registered client.
func remoteOAuthProvider(definition api.ToolDefinition) string {
	return "remote-mcp:" + definition.DefinitionID
}

// remoteOAuthContract is the RFC6750 bearer delivery the minted access token
// uses at call time. It is the protocol's own delivery, not a provider guess:
// every protected resource answerable through this flow takes the token on
// Authorization: Bearer.
func remoteOAuthContract() []api.CredentialInput {
	return []api.CredentialInput{{Name: "ACCESS_TOKEN", Required: true, Delivery: "http_header", Target: "Authorization", Prefix: "Bearer "}}
}

// prepareRemoteOAuth offers the browser flow when an auth-required remote
// endpoint advertises RFC9728 protected-resource metadata. advertised=false
// means the endpoint says nothing about OAuth and the static-credential
// instruction stands.
func (c *ControlPlane) prepareRemoteOAuth(auth identity.Envelope, draft api.ToolDefinition, onboardingID, requestKey string, allowlist []string) (map[string]any, bool, error) {
	if c.OAuth == nil || c.Secrets == nil {
		return nil, false, nil
	}
	if _, err := c.OAuth.DiscoverRemoteMCPAs(remoteOAuthProvider(draft), draft.Source.URL); err != nil {
		return nil, false, nil
	}
	// The draft carries no tools yet — tools/list only answers after the
	// OAuth credential exists. admitRemoteDefinition validates the real
	// admitted definition post-callback, same as the form-credential path.
	draft.Credentials = remoteOAuthContract()
	draft.Tools = api.RemotePlaceholders(allowlist)
	onboarding := api.Onboarding{
		Schema: api.SchemaVersion, OnboardingID: onboardingID,
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: api.OnboardingRemote, Phase: api.PhaseAwaitingOAuth, SourceURL: draft.Source.URL,
		DefinitionID: draft.DefinitionID, DefinitionVersion: draft.Version,
		// ACCESS_TOKEN is the name the credential reference keys on; the flow
		// mints it, so the form never asks for it.
		Required:         []api.CredentialHint{{Name: "ACCESS_TOKEN", Type: "oauth", Secret: true, Delivery: "http_header", Target: "Authorization", Hint: "browser OAuth"}},
		AdmissionPending: true,
		Error:            "The remote endpoint requires sign-in. Call status to open the authorization link.",
		IdempotencyKey:   requestKey, Revision: 1, CreatedAt: c.now(),
		Definition: &draft,
	}
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, true, err
	}
	return c.statusBody(onboarding, false), true, nil
}

// ensureRemoteOAuth is the generic sibling of ensurePreparedOAuth: discovery
// already recorded the provider, RFC7591 registration supplies a client when
// the AS offers one, and StartAuth carries PKCE plus the RFC8707 resource.
func (c *ControlPlane) ensureRemoteOAuth(auth identity.Envelope, onboarding api.Onboarding, definition api.ToolDefinition) (map[string]any, error) {
	if c.OAuth == nil || c.Secrets == nil || definition.Transport != api.RemoteMCP || definition.Source.URL == "" {
		return nil, api.ErrUnauthorized
	}
	c.oauthFlows.mu.Lock()
	defer c.oauthFlows.mu.Unlock()
	for state, flow := range c.oauthFlows.flows {
		if !c.now().Before(flow.expires) {
			delete(c.oauthFlows.flows, state)
			continue
		}
		if flow.remote && flow.onboardingID == onboarding.OnboardingID {
			onboarding.Phase = api.PhaseAwaitingOAuth
			onboarding.ProviderAuthorizationURL = flow.url
			return c.statusBody(onboarding, false), nil
		}
	}
	provider := remoteOAuthProvider(definition)
	redirect := c.origin() + "/oauth/callback"
	if _, err := c.OAuth.DiscoverRemoteMCPAs(provider, definition.Source.URL); err != nil {
		return nil, fmt.Errorf("%w: remote endpoint advertises no usable OAuth metadata", api.ErrUnsupportedAuth)
	}
	if c.OAuth.Client(provider).ID == "" {
		if prov, ok := c.OAuth.Provider(provider); ok && prov.RegistrationURL != "" {
			if _, err := c.OAuth.RegisterClient(provider, redirect); err != nil {
				return nil, fmt.Errorf("%w: dynamic client registration: %v", api.ErrUnauthorized, err)
			}
		}
	}
	start, err := c.OAuth.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: provider, Redirect: redirect})
	if err != nil {
		return nil, err
	}
	if !validPreparedOAuthURL(start.AuthorizeURL) {
		return nil, api.ErrInvalid
	}
	if c.oauthFlows.flows == nil {
		c.oauthFlows.flows = make(map[string]*preparedOAuthFlow)
	}
	c.oauthFlows.flows[start.State] = &preparedOAuthFlow{onboardingID: onboarding.OnboardingID, url: start.AuthorizeURL, expires: c.now().Add(c.ttl()), broker: c.OAuth, remote: true}
	onboarding.Phase = api.PhaseAwaitingOAuth
	onboarding.ProviderAuthorizationURL = start.AuthorizeURL
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		delete(c.oauthFlows.flows, start.State)
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

// completeRemoteOAuth finishes the browser flow for a generic remote MCP
// endpoint: exchange the code, park the token set in the owner's credential
// locator, re-probe tools/list with the minted bearer, then confirm+enable
// exactly like a form-submitted credential.
func (c *ControlPlane) completeRemoteOAuth(ctx context.Context, flow *preparedOAuthFlow, onboarding api.Onboarding, state, code string) error {
	draftPtr := onboarding.Definition
	if draftPtr == nil || draftPtr.Transport != api.RemoteMCP {
		return api.ErrUnauthorized
	}
	draft := *draftPtr
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	meta, err := flow.broker.HandleCallback(auth.PrincipalID, auth.ContextID, onboarding.OnboardingID, state, code, c.origin()+"/oauth/callback")
	if err != nil {
		return err
	}
	values, err := c.Secrets.Get(meta.Locator, auth.PrincipalID)
	if err != nil {
		return err
	}
	access := values["ACCESS_TOKEN"]
	if access == "" || values["REFRESH_TOKEN"] == "" {
		return fmt.Errorf("%w: token exchange returned no usable token set", api.ErrUnauthorized)
	}
	// Persist the discovered provider document and DCR client beside the
	// tokens: refresh must survive a hub restart, so the locator is the
	// durable record, not broker memory.
	provider := remoteOAuthProvider(draft)
	if prov, ok := flow.broker.Provider(provider); ok {
		if doc, merr := json.Marshal(prov); merr == nil {
			values["OAUTH_PROVIDER_DOC"] = string(doc)
		}
	}
	if client := flow.broker.Client(provider); client.ID != "" {
		values["OAUTH_CLIENT_ID"] = client.ID
		if client.Secret != "" {
			values["OAUTH_CLIENT_SECRET"] = client.Secret
		}
	}
	if err := c.Secrets.Put(meta.Locator, auth.PrincipalID, values); err != nil {
		return err
	}
	for key := range values {
		values[key] = ""
	}
	probeCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	admitted, admitErr := admitRemoteDefinition(probeCtx, draft, map[string]string{"ACCESS_TOKEN": access})
	cancel()
	if admitErr != nil {
		return c.persistFailure(onboarding, api.PublicPrepareError(admitErr), admitErr)
	}
	if err := c.registerAdmittedDefinition(&admitted, onboarding.PrincipalID); err != nil {
		return err
	}
	if admitted.Workload.Class == api.Shared {
		if err := c.Store.PromoteToCatalog(admitted.DefinitionID, admitted.Version, onboarding.PrincipalID); err != nil {
			return err
		}
	}
	onboarding.Definition = &admitted
	onboarding.DefinitionID = admitted.DefinitionID
	onboarding.DefinitionVersion = admitted.Version
	onboarding.Permissions = api.ToolNames(admitted)
	onboarding.Effects = api.EffectNames(admitted)
	onboarding.AdmissionPending = false
	onboarding.Locator = meta.Locator
	onboarding.CredentialOwner = auth.PrincipalID
	onboarding.OAuthProvider = provider
	onboarding.Phase = api.PhaseAwaitingConfirm
	onboarding.ProviderAuthorizationURL = ""
	onboarding.Error = ""
	onboarding.ErrorCode = ""
	onboarding.ConfirmationNonce = ""
	onboarding.ConfirmationExpires = time.Time{}
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	if err := c.refreshConfirmation(&onboarding); err != nil {
		return err
	}
	if _, err := c.confirm(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID, "nonce": onboarding.ConfirmationNonce}); err != nil {
		return err
	}
	if _, err := c.enable(ctx, auth, map[string]any{"onboarding_id": onboarding.OnboardingID}); err != nil {
		return err
	}
	return nil
}

// remoteOAuthRefresh mints a fresh access token for an OAuth-managed remote
// connection whose call was rejected. The provider document and DCR client
// are restored from the owner's locator — the durable record — so refresh
// works across hub restarts without re-discovery.
func remoteOAuthRefresh(secrets credstore.Backend, broker *oauth.Broker) func(context.Context, api.EffectiveBinding) (bool, error) {
	return func(_ context.Context, effective api.EffectiveBinding) (bool, error) {
		if effective.Connection == nil || effective.Credential == nil {
			return false, nil
		}
		owner := effective.Binding.PrincipalID
		if effective.Connection.Owner.ID != "" {
			owner = effective.Connection.Owner.ID
		}
		if shared := effective.Connection.Metadata["credential_owner"]; shared != "" {
			owner = shared
		}
		stored, err := secrets.Get(effective.Credential.Locator, owner)
		if err != nil {
			return false, err
		}
		// Copy the keys this restore needs: the returned map may be shared
		// with other readers, so it is never mutated or cleared in place. The
		// locator is authoritative — shared connections carry no metadata
		// markers, so OAUTH_PROVIDER/OAUTH_CONNECTION come from the record.
		values := map[string]string{
			"REFRESH_TOKEN":       stored["REFRESH_TOKEN"],
			"OAUTH_PROVIDER":      stored["OAUTH_PROVIDER"],
			"OAUTH_CONNECTION":    stored["OAUTH_CONNECTION"],
			"OAUTH_PROVIDER_DOC":  stored["OAUTH_PROVIDER_DOC"],
			"OAUTH_CLIENT_ID":     stored["OAUTH_CLIENT_ID"],
			"OAUTH_CLIENT_SECRET": stored["OAUTH_CLIENT_SECRET"],
		}
		defer func() {
			for key := range values {
				values[key] = ""
			}
		}()
		provider := values["OAUTH_PROVIDER"]
		if provider == "" {
			provider = effective.Connection.Metadata["oauth_provider"]
		}
		oauthConnection := values["OAUTH_CONNECTION"]
		if oauthConnection == "" {
			oauthConnection = effective.Connection.Metadata["oauth_connection"]
		}
		if values["REFRESH_TOKEN"] == "" || provider == "" || oauthConnection == "" {
			return false, nil
		}
		if _, ok := broker.Provider(provider); !ok {
			var prov oauth.Provider
			if err := json.Unmarshal([]byte(values["OAUTH_PROVIDER_DOC"]), &prov); err != nil || prov.TokenURL == "" || prov.AuthorizeURL == "" {
				return false, fmt.Errorf("%w: oauth provider record", api.ErrStale)
			}
			broker.RestoreProvider(provider, prov)
		}
		if broker.Client(provider).ID == "" && values["OAUTH_CLIENT_ID"] != "" {
			broker.RestoreClient(provider, oauth.ClientCredential{ID: values["OAUTH_CLIENT_ID"], Secret: values["OAUTH_CLIENT_SECRET"]})
		}
		if _, err := broker.Refresh(owner, oauthConnection, provider); err != nil {
			return false, err
		}
		return true, nil
	}
}
