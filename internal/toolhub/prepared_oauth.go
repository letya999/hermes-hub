package toolhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type preparedOAuthFlow struct {
	onboardingID string
	url          string
	expires      time.Time
	broker       *oauth.Broker
}

// ensurePreparedOAuth starts only a reviewed OAuth handoff. Tool output is
// never interpreted as an authorization URL or a provider configuration.
func (c *ControlPlane) ensurePreparedOAuth(ctx context.Context, auth identity.Envelope, onboarding Onboarding, definition ToolDefinition) (map[string]any, error) {
	entry, matched, err := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA})
	if err != nil || !matched || entry.OAuth == nil || c.Injector == nil || c.Secrets == nil {
		return nil, ErrUnauthorized
	}
	c.oauthMu.Lock()
	// ponytail: one lock serializes handoff creation; use per-onboarding locks if starts become a bottleneck.
	defer c.oauthMu.Unlock()
	for state, flow := range c.oauthFlows {
		if !c.now().Before(flow.expires) {
			delete(c.oauthFlows, state)
			continue
		}
		if flow.onboardingID == onboarding.OnboardingID {
			onboarding.Phase = PhaseAwaitingOAuth
			onboarding.ProviderAuthorizationURL = flow.url
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
		return nil, ErrIsolation
	}
	if _, err := os.Stat(filepath.Join(stateDir, entry.OAuth.TokenFile)); err == nil {
		return nil, fmt.Errorf("%w: existing token state requires diagnosis", ErrUnauthorized)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	data, err := os.ReadFile(clientFile)
	if err != nil || len(data) > 65536 {
		return nil, ErrIsolation
	}
	defer clear(data)
	var document struct {
		Installed *struct {
			ID     string   `json:"client_id"`
			Secret string   `json:"client_secret"`
			URIs   []string `json:"redirect_uris"`
		} `json:"installed"`
		Web *struct {
			ID     string   `json:"client_id"`
			Secret string   `json:"client_secret"`
			URIs   []string `json:"redirect_uris"`
		} `json:"web"`
	}
	if json.Unmarshal(data, &document) != nil {
		return nil, ErrInvalid
	}
	redirect := c.origin() + "/oauth/callback"
	client := document.Installed
	if client == nil {
		client = document.Web
		if client == nil {
			return nil, ErrInvalid
		}
		allowed := false
		for _, uri := range client.URIs {
			allowed = allowed || uri == redirect
		}
		if !allowed {
			return nil, fmt.Errorf("%w: OAuth web client callback is not registered", ErrInvalid)
		}
	}
	if client.ID == "" || client.Secret == "" {
		return nil, ErrInvalid
	}
	if _, ok := oauth.OfficialProviders()[entry.OAuth.Provider]; !ok {
		return nil, ErrInvalid
	}
	broker := oauth.NewBroker(c.Secrets, []string{redirect})
	broker.Clients[entry.OAuth.Provider] = oauth.ClientCredential{ID: client.ID, Secret: client.Secret}
	start, err := broker.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: entry.OAuth.Provider, Redirect: redirect, Scopes: entry.OAuth.Scopes, AccessType: "offline", Prompt: "consent"})
	if err != nil {
		return nil, err
	}
	if !validPreparedOAuthURL(start.AuthorizeURL) {
		return nil, ErrInvalid
	}
	if c.oauthFlows == nil {
		c.oauthFlows = make(map[string]*preparedOAuthFlow)
	}
	c.oauthFlows[start.State] = &preparedOAuthFlow{onboardingID: onboarding.OnboardingID, url: start.AuthorizeURL, expires: c.now().Add(c.ttl()), broker: broker}
	onboarding.Phase = PhaseAwaitingOAuth
	onboarding.ProviderAuthorizationURL = start.AuthorizeURL
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		delete(c.oauthFlows, start.State)
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) preparedOAuthEffective(auth identity.Envelope, definition ToolDefinition) (EffectiveBinding, error) {
	c.Store.mu.RLock()
	matches := c.Store.matchingOwnerConnectionsLocked(auth, definition)
	c.Store.mu.RUnlock()
	if len(matches) != 1 || matches[0].credential.BrokerGrantID == "" {
		return EffectiveBinding{}, ErrUnauthorized
	}
	connection, credential := matches[0].connection, matches[0].credential
	binding := ToolBinding{Schema: SchemaVersion, PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version, ConnectionID: connection.ConnectionID, ConnectionRevision: connection.Revision, CredentialRefID: credential.CredentialRefID, CredentialRevision: credential.Revision, PolicyVersion: auth.PolicyVersion, WorkloadClass: definition.Workload.Class, Status: ActiveStatus, Revision: 1, ProjectionRevision: 1}
	binding.ToolBindingID = DeterministicBindingID(binding.PrincipalID, binding.ContextID, binding.RuntimeID, binding.DefinitionID, binding.DefinitionVersion, binding.ConnectionID, binding.CredentialRefID)
	ownerID := auth.ContextID + ":" + auth.PrincipalID + ":" + connection.ConnectionID
	return EffectiveBinding{Binding: binding, Definition: definition, Connection: &connection, Credential: &credential, WorkloadID: WorkloadInstanceID(definition.DefinitionID, definition.Workload.Class, ownerID, "")}, nil
}

func (c *ControlPlane) completePreparedOAuth(ctx context.Context, state, code string) error {
	c.oauthMu.Lock()
	flow := c.oauthFlows[state]
	delete(c.oauthFlows, state)
	c.oauthMu.Unlock()
	if flow == nil || !c.now().Before(flow.expires) || code == "" {
		return ErrUnauthorized
	}
	onboarding, err := c.Store.onboarding(flow.onboardingID)
	if err != nil || onboarding.Phase != PhaseAwaitingOAuth {
		return ErrUnauthorized
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
	entry, matched, err := preparedForSource(ArtifactSource{Repository: definition.Source.Repository, Subfolder: definition.Source.Subfolder, CommitSHA: definition.Source.CommitSHA})
	if err != nil || !matched || entry.OAuth == nil {
		return ErrUnauthorized
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
		return ErrIsolation
	}
	seconds, err := strconv.Atoi(values["EXPIRES_IN"])
	if err != nil || seconds <= 0 {
		return ErrInvalid
	}
	token := map[string]any{"access_token": values["ACCESS_TOKEN"], "refresh_token": values["REFRESH_TOKEN"], "expiry_date": c.now().Add(time.Duration(seconds) * time.Second).UnixMilli(), "token_type": values["TOKEN_TYPE"], "scope": values["OAUTH_SCOPE"]}
	encoded, err := json.Marshal(map[string]any{entry.OAuth.TokenAccount: token})
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
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ProviderAuthorizationURL = ""
	onboarding.ConfirmationNonce = ""
	onboarding.ConfirmationExpires = time.Time{}
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return err
	}
	if err := c.refreshConfirmation(&onboarding); err != nil {
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
