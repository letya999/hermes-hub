package toolhub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/credstore"
	"github.com/letya999/hermes-hub/internal/identity"
	"github.com/letya999/hermes-hub/internal/oauth"
)

type SourceReview struct {
	Definition   ToolDefinition
	Permissions  []string
	Effects      []string
	ReviewDigest string
}

type SourceReviewer func(context.Context, ArtifactSource) (SourceReview, error)

type ControlPlane struct {
	Store           *Store
	Secrets         credstore.Backend
	Reviewer        SourceReviewer
	OAuth           *oauth.Broker
	Now             func() time.Time
	Listen          string
	FormOrigin      string
	WorkloadRoot    string
	ConfirmationTTL time.Duration
}

func (c *ControlPlane) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *ControlPlane) ttl() time.Duration {
	if c != nil && c.ConfirmationTTL > 0 {
		return c.ConfirmationTTL
	}
	return 10 * time.Minute
}

func (c *ControlPlane) origin() string {
	if c != nil && c.FormOrigin != "" {
		return strings.TrimRight(c.FormOrigin, "/")
	}
	if c != nil && c.Listen != "" {
		if strings.Contains(c.Listen, "://") {
			return strings.TrimRight(c.Listen, "/")
		}
		return "http://" + c.Listen
	}
	return "http://127.0.0.1"
}

func (c *ControlPlane) Invoke(ctx context.Context, auth identity.Envelope, op string, args map[string]any) (map[string]any, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("%w: control plane", ErrInvalid)
	}
	if err := RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	switch op {
	case "prepare_source":
		return c.prepareSource(ctx, auth, args)
	case "status":
		return c.status(auth, args)
	case "required_credentials":
		return c.requiredCredentials(auth, args)
	case "confirm":
		return c.confirm(auth, args)
	case "enable":
		return c.enable(auth, args)
	case "disable":
		return c.setPhase(auth, args, DisabledStatus, PhaseDisabled)
	case "revoke":
		return c.revoke(auth, args)
	case "remove":
		return c.remove(auth, args)
	default:
		return nil, fmt.Errorf("%w: unknown control operation", ErrInvalid)
	}
}

func (c *ControlPlane) prepareSource(ctx context.Context, auth identity.Envelope, args map[string]any) (map[string]any, error) {
	requestKey := argString(args, "request_key")
	if requestKey != "" {
		if existing, ok := c.Store.FindOnboardingByKey(auth, requestKey); ok {
			return c.statusBody(existing, false), nil
		}
	}
	source := argString(args, "source")
	definitionID := argString(args, "definition_id")
	version := argString(args, "version")
	if source != "" {
		return c.prepareSelfInstall(ctx, auth, source, requestKey)
	}
	if definitionID != "" && version != "" {
		return c.prepareCatalog(auth, definitionID, version, requestKey)
	}
	return nil, fmt.Errorf("%w: source or definition required", ErrInvalid)
}

func (c *ControlPlane) prepareCatalog(auth identity.Envelope, definitionID, version, requestKey string) (map[string]any, error) {
	definition, err := c.Store.Definition(definitionID, version)
	if err != nil {
		return nil, err
	}
	if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, OnboardingCatalog, requestKey, definition, "", "")
	if err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) prepareSelfInstall(ctx context.Context, auth identity.Envelope, sourceURL, requestKey string) (map[string]any, error) {
	if err := c.Store.RequireSelfInstall(auth); err != nil {
		return nil, err
	}
	source, err := ParseGitHubSource(sourceURL)
	if err != nil {
		return nil, err
	}
	if c.Reviewer == nil {
		return nil, fmt.Errorf("%w: source reviewer unavailable", ErrInvalid)
	}
	review, err := c.Reviewer(ctx, source)
	if err != nil {
		return nil, err
	}
	if err := review.Definition.Validate(); err != nil {
		return nil, err
	}
	if err := c.Store.RegisterDefinition(review.Definition); err != nil {
		return nil, err
	}
	if err := c.Store.PutPublication(DefinitionPublication{DefinitionID: review.Definition.DefinitionID, Version: review.Definition.Version, Visibility: PublicationUser, OwnerPrincipalID: auth.PrincipalID}); err != nil {
		return nil, err
	}
	onboarding, err := c.newOnboarding(auth, OnboardingSelfInstall, requestKey, review.Definition, source.Repository, source.CommitSHA)
	if err != nil {
		return nil, err
	}
	onboarding.Permissions = append([]string(nil), review.Permissions...)
	onboarding.Effects = append([]string(nil), review.Effects...)
	onboarding.ReviewDigest = review.ReviewDigest
	copyDef := review.Definition
	onboarding.Definition = &copyDef
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) newOnboarding(auth identity.Envelope, mode, requestKey string, definition ToolDefinition, sourceURL, commit string) (Onboarding, error) {
	idSeed := requestKey
	if idSeed == "" {
		idSeed = mode + ":" + definition.DefinitionID + "@" + definition.Version + ":" + sourceURL + ":" + commit
	}
	onboarding := Onboarding{
		Schema: SchemaVersion, OnboardingID: deterministicID("onboard", auth.PrincipalID, auth.ContextID, auth.RuntimeID, idSeed),
		PrincipalID: auth.PrincipalID, ContextID: auth.ContextID, RuntimeID: auth.RuntimeID, PolicyVersion: auth.PolicyVersion,
		Mode: mode, SourceURL: sourceURL, CommitSHA: commit, DefinitionID: definition.DefinitionID, DefinitionVersion: definition.Version,
		Required: credentialHints(definition), Permissions: toolNames(definition), Effects: effectNames(definition),
		IdempotencyKey: requestKey, Revision: 1, CreatedAt: c.now(),
	}
	if len(onboarding.Required) > 0 {
		onboarding.Phase = PhaseAwaitingCreds
		onboarding.FormNonce = randomNonce()
	} else {
		onboarding.Phase = PhaseAwaitingConfirm
		onboarding.ConfirmationNonce = randomNonce()
		onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	}
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return Onboarding{}, err
	}
	return onboarding, nil
}

func (c *ControlPlane) status(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) requiredCredentials(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	body := c.statusBody(onboarding, true)
	body["input"] = "localhost-form"
	body["form_url"] = c.origin() + "/credentials/" + onboarding.OnboardingID + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
	if c.OAuth != nil {
		body["oauth"] = "pkce"
	}
	return body, nil
}

func (c *ControlPlane) confirm(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := rejectEscalation(definition, args); err != nil {
		return nil, err
	}
	nonce := argString(args, "nonce")
	if onboarding.ConfirmationUsed {
		return nil, fmt.Errorf("%w: replayed nonce", ErrUnauthorized)
	}
	if onboarding.ConfirmationNonce == "" || nonce != onboarding.ConfirmationNonce {
		return nil, fmt.Errorf("%w: confirmation nonce", ErrUnauthorized)
	}
	if onboarding.ConfirmationExpires.IsZero() || c.now().After(onboarding.ConfirmationExpires) {
		return nil, fmt.Errorf("%w: expired confirmation", ErrUnauthorized)
	}
	if len(onboarding.Required) > 0 && onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: credentials required", ErrUnauthorized)
	}
	binding, err := c.materializeBinding(auth, onboarding, definition)
	if err != nil {
		return nil, err
	}
	onboarding.ConfirmationUsed = true
	onboarding.BindingID = binding.ToolBindingID
	onboarding.ConnectionID = binding.ConnectionID
	onboarding.CredentialRefID = binding.CredentialRefID
	onboarding.Phase = PhaseConfirmed
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) enable(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		definitionID := argString(args, "definition_id")
		version := argString(args, "version")
		if definitionID == "" || version == "" {
			return nil, err
		}
		definition, defErr := c.Store.Definition(definitionID, version)
		if defErr != nil {
			return nil, defErr
		}
		if err := rejectEscalation(definition, args); err != nil {
			return nil, err
		}
		if pub := c.Store.publicationFor(definition); pub.Visibility != PublicationUser {
			if err := c.Store.RequireCatalogAccess(auth, definition); err != nil {
				return nil, err
			}
		}
		binding, enableErr := c.Store.Enable(auth, definitionID, version)
		if enableErr != nil {
			return nil, enableErr
		}
		return map[string]any{"phase": PhaseEnabled, "status": string(binding.Status), "binding_id": binding.ToolBindingID, "definition_id": binding.DefinitionID, "version": binding.DefinitionVersion}, nil
	}
	definition, err := c.definitionOf(onboarding)
	if err != nil {
		return nil, err
	}
	if err := rejectEscalation(definition, args); err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		if onboarding.Phase != PhaseConfirmed && onboarding.Phase != PhaseEnabled && onboarding.Phase != PhaseDisabled {
			return nil, fmt.Errorf("%w: confirm required", ErrUnauthorized)
		}
		binding, err := c.materializeBinding(auth, onboarding, definition)
		if err != nil {
			return nil, err
		}
		onboarding.BindingID = binding.ToolBindingID
		onboarding.ConnectionID = binding.ConnectionID
		onboarding.CredentialRefID = binding.CredentialRefID
	}
	existing, err := c.Store.binding(onboarding.BindingID)
	if err != nil {
		return nil, err
	}
	if existing.Status == RevokedStatus {
		return nil, ErrRevoked
	}
	if existing.Status != ActiveStatus {
		if err := c.Store.SetBindingStatus(onboarding.BindingID, ActiveStatus); err != nil {
			return nil, err
		}
	}
	onboarding.Phase = PhaseEnabled
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) setPhase(auth identity.Envelope, args map[string]any, status Status, phase string) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.BindingID == "" {
		return nil, fmt.Errorf("%w: binding", ErrNotFound)
	}
	if err := c.Store.SetBindingStatus(onboarding.BindingID, status); err != nil {
		return nil, err
	}
	onboarding.Phase = phase
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) revoke(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if err := c.cutAuthorization(onboarding); err != nil {
		return nil, err
	}
	onboarding.Phase = PhaseRevoked
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) remove(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	_ = c.cutAuthorization(onboarding)
	if onboarding.BindingID != "" {
		if binding, err := c.Store.binding(onboarding.BindingID); err == nil {
			definition, _ := c.definitionOf(onboarding)
			effective := EffectiveBinding{Binding: binding, Definition: definition}
			if binding.ConnectionID != "" {
				if conn, _, err := c.Store.OwnedConnection(auth, binding.ConnectionID); err == nil {
					effective.Connection = &conn
				}
			}
			if err := RemoveBindingWorkspace(c.WorkloadRoot, effective); err != nil {
				return nil, err
			}
		}
	}
	onboarding.Phase = PhaseRemoved
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) Rotate(auth identity.Envelope, args map[string]any) (map[string]any, error) {
	if err := RejectAuthorityArguments(args); err != nil {
		return nil, err
	}
	onboarding, err := c.resolveOnboarding(auth, args)
	if err != nil {
		return nil, err
	}
	if onboarding.ConnectionID == "" || onboarding.Locator == "" {
		return nil, fmt.Errorf("%w: connection", ErrNotFound)
	}
	keys := credentialNames(onboarding)
	if binding, err := c.Store.binding(onboarding.BindingID); err == nil && binding.CredentialRefID != "" {
		c.Store.mu.RLock()
		if ref, ok := c.Store.credentials[binding.CredentialRefID]; ok {
			keys = append([]string(nil), ref.Keys...)
		}
		c.Store.mu.RUnlock()
	}
	next, err := c.Store.RotateCredential(onboarding.ConnectionID, credstore.BackendLocal, onboarding.Locator+"-rotated", keys)
	if err != nil {
		return nil, err
	}
	_ = c.Store.SetBindingStatus(onboarding.BindingID, RevokedStatus)
	onboarding.CredentialRefID = next.CredentialRefID
	onboarding.Phase = PhaseRevoked
	onboarding.Revision++
	if err := c.Store.PutOnboarding(onboarding); err != nil {
		return nil, err
	}
	return c.statusBody(onboarding, false), nil
}

func (c *ControlPlane) cutAuthorization(onboarding Onboarding) error {
	if onboarding.BindingID != "" {
		if err := c.Store.SetBindingStatus(onboarding.BindingID, RevokedStatus); err != nil && !errors.Is(err, ErrRevoked) && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	if onboarding.ConnectionID != "" {
		_ = c.Store.SetConnectionStatus(onboarding.ConnectionID, RevokedStatus)
	}
	if c.Secrets != nil && onboarding.Locator != "" {
		owner := onboarding.PrincipalID
		if onboarding.CredentialOwner != "" {
			owner = onboarding.CredentialOwner
		}
		_ = c.Secrets.SetStatus(onboarding.Locator, owner, credstore.StatusRevoked)
	}
	return nil
}

func (c *ControlPlane) materializeBinding(auth identity.Envelope, onboarding Onboarding, definition ToolDefinition) (ToolBinding, error) {
	if existing := c.Store.findBinding(auth, definition); existing != nil && existing.Status != RevokedStatus {
		return *existing, nil
	}
	binding, err := c.Store.Enable(auth, definition.DefinitionID, definition.Version)
	if err == nil {
		return binding, nil
	}
	if len(definition.Credentials) == 0 || onboarding.Locator == "" {
		return ToolBinding{}, err
	}
	connectionID := deterministicID("conn", auth.PrincipalID, definition.DefinitionID, onboarding.OnboardingID)
	reference := CredentialReference{Schema: SchemaVersion, CredentialRefID: CredentialReferenceID(connectionID, 1), ConnectionID: connectionID, Revision: 1, Backend: credstore.BackendLocal, Locator: onboarding.Locator, Keys: credentialNames(onboarding), Status: ActiveStatus}
	if err := c.Store.PutCredentialReference(reference); err != nil {
		return ToolBinding{}, err
	}
	connection := Connection{Schema: SchemaVersion, ConnectionID: connectionID, Owner: OwnerRef{Type: PrincipalOwner, ID: auth.PrincipalID}, DefinitionID: definition.DefinitionID, CredentialRefID: reference.CredentialRefID, Revision: 1, Status: ActiveStatus}
	if onboarding.CredentialOwner != "" && onboarding.CredentialOwner != auth.PrincipalID {
		connection.Metadata = map[string]string{"credential_owner": onboarding.CredentialOwner}
	}
	if err := c.Store.PutConnection(connection); err != nil {
		return ToolBinding{}, err
	}
	return c.Store.Enable(auth, definition.DefinitionID, definition.Version)
}

func (c *ControlPlane) SubmitCredentials(onboardingID, nonce string, values map[string]string) error {
	if c == nil || c.Store == nil || c.Secrets == nil {
		return fmt.Errorf("%w: credential store", ErrInvalid)
	}
	onboarding, err := c.Store.onboarding(onboardingID)
	if err != nil {
		return err
	}
	if onboarding.FormNonce == "" || nonce != onboarding.FormNonce {
		return fmt.Errorf("%w: form nonce", ErrUnauthorized)
	}
	if onboarding.Phase != PhaseAwaitingCreds && onboarding.Phase != PhaseAwaitingConfirm {
		return fmt.Errorf("%w: credentials not expected", ErrUnauthorized)
	}
	filtered := map[string]string{}
	for _, hint := range onboarding.Required {
		value := strings.TrimSpace(values[hint.Name])
		if value == "" {
			return fmt.Errorf("%w: missing %s", ErrInvalid, hint.Name)
		}
		filtered[hint.Name] = value
	}
	locator := onboarding.Locator
	owner := onboarding.PrincipalID
	shared := false
	c.Store.mu.RLock()
	auth := identity.Envelope{Schema: identity.Schema, PrincipalID: onboarding.PrincipalID, ExternalIdentityID: onboarding.PrincipalID, ContextID: onboarding.ContextID, RuntimeID: onboarding.RuntimeID, ConversationID: onboarding.PrincipalID, DeliveryTargetID: onboarding.PrincipalID, PolicyVersion: onboarding.PolicyVersion}
	if policy := c.Store.sharedPolicyLocked(auth, onboarding.DefinitionID); policy != nil {
		locator = policy.Locator
		owner = policy.StoreOwner
		onboarding.CredentialOwner = policy.StoreOwner
		shared = true
	}
	c.Store.mu.RUnlock()
	if !shared {
		if locator == "" {
			locator, err = c.Secrets.NewLocator()
			if err != nil {
				return err
			}
		}
		if err := c.Secrets.Put(locator, owner, filtered); err != nil {
			return err
		}
	} else if locator == "" {
		return fmt.Errorf("%w: shared locator", ErrInvalid)
	}
	onboarding.Locator = locator
	onboarding.CredentialOwner = owner
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.ConfirmationUsed = false
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

func (c *ControlPlane) StartOAuth(auth identity.Envelope, onboardingID, redirect string) (oauth.StartResult, error) {
	if c == nil || c.OAuth == nil {
		return oauth.StartResult{}, fmt.Errorf("%w: oauth broker", ErrInvalid)
	}
	onboarding, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return oauth.StartResult{}, err
	}
	return c.OAuth.StartAuth(oauth.AuthRequest{Principal: auth.PrincipalID, Context: auth.ContextID, Connection: onboarding.OnboardingID, Provider: "fixture", Redirect: redirect})
}

func (c *ControlPlane) HandleOAuthCallback(auth identity.Envelope, onboardingID, state, code, redirect string) error {
	if c == nil || c.OAuth == nil {
		return fmt.Errorf("%w: oauth broker", ErrInvalid)
	}
	onboarding, err := c.Store.OnboardingFor(auth, onboardingID)
	if err != nil {
		return err
	}
	meta, err := c.OAuth.HandleCallback(auth.PrincipalID, auth.ContextID, onboarding.OnboardingID, state, code, redirect)
	if err != nil {
		return err
	}
	onboarding.Locator = meta.Locator
	onboarding.CredentialOwner = auth.PrincipalID
	onboarding.Phase = PhaseAwaitingConfirm
	onboarding.ConfirmationNonce = randomNonce()
	onboarding.ConfirmationExpires = c.now().Add(c.ttl())
	onboarding.Revision++
	return c.Store.PutOnboarding(onboarding)
}

func (c *ControlPlane) resolveOnboarding(auth identity.Envelope, args map[string]any) (Onboarding, error) {
	if id := argString(args, "onboarding_id"); id != "" {
		return c.Store.OnboardingFor(auth, id)
	}
	definitionID := argString(args, "definition_id")
	version := argString(args, "version")
	if definitionID == "" {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	c.Store.mu.RLock()
	defer c.Store.mu.RUnlock()
	var found Onboarding
	ok := false
	for _, onboarding := range c.Store.onboardings {
		if onboarding.PrincipalID == auth.PrincipalID && onboarding.ContextID == auth.ContextID && onboarding.RuntimeID == auth.RuntimeID && onboarding.PolicyVersion == auth.PolicyVersion && onboarding.DefinitionID == definitionID && (version == "" || onboarding.DefinitionVersion == version) && onboarding.Phase != PhaseRemoved {
			if ok && onboarding.CreatedAt.Before(found.CreatedAt) {
				continue
			}
			found, ok = onboarding, true
		}
	}
	if !ok {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	return found, nil
}

func (c *ControlPlane) definitionOf(onboarding Onboarding) (ToolDefinition, error) {
	if onboarding.Definition != nil {
		return *onboarding.Definition, nil
	}
	return c.Store.Definition(onboarding.DefinitionID, onboarding.DefinitionVersion)
}

func (c *ControlPlane) statusBody(onboarding Onboarding, withHints bool) map[string]any {
	body := map[string]any{
		"onboarding_id":  onboarding.OnboardingID,
		"phase":          onboarding.Phase,
		"status":         onboarding.Phase,
		"mode":           onboarding.Mode,
		"definition_id":  onboarding.DefinitionID,
		"version":        onboarding.DefinitionVersion,
		"binding_id":     onboarding.BindingID,
		"permissions":    onboarding.Permissions,
		"effects":        onboarding.Effects,
		"principal_from": "request",
	}
	if withHints {
		hints := make([]map[string]string, 0, len(onboarding.Required))
		for _, hint := range onboarding.Required {
			hints = append(hints, map[string]string{"name": hint.Name, "type": hint.Type, "hint": hint.Hint})
		}
		body["credentials"] = hints
		if onboarding.FormNonce != "" {
			body["form_path"] = "/credentials/" + onboarding.OnboardingID + "?nonce=" + url.QueryEscape(onboarding.FormNonce)
			body["form_url"] = c.origin() + body["form_path"].(string)
		}
	}
	if onboarding.Phase == PhaseAwaitingConfirm && onboarding.ConfirmationNonce != "" && !onboarding.ConfirmationUsed {
		body["nonce"] = onboarding.ConfirmationNonce
	}
	return body
}

func (s *Store) Definition(id, version string) (ToolDefinition, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	definition, ok := s.definitions[definitionKey(id, version)]
	if !ok {
		return ToolDefinition{}, fmt.Errorf("%w: definition", ErrNotFound)
	}
	return definition, nil
}

func (s *Store) publicationFor(definition ToolDefinition) DefinitionPublication {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicationLocked(definition)
}

func (s *Store) onboarding(id string) (Onboarding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	return onboarding, nil
}

func (s *Store) binding(id string) (ToolBinding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding, ok := s.bindings[id]
	if !ok {
		return ToolBinding{}, fmt.Errorf("%w: binding", ErrNotFound)
	}
	return binding, nil
}

func (s *Store) findBinding(auth identity.Envelope, definition ToolDefinition) *ToolBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findBindingLocked(auth, definition)
}

func (s *Store) WorkloadIDsForBinding(bindingID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0)
	for _, workload := range s.workloads {
		if workload.BindingID == bindingID {
			ids = append(ids, workload.WorkloadID)
		}
	}
	return ids
}

func ParseGitHubSource(raw string) (ArtifactSource, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && (parts[2] == "commit" || parts[2] == "tree") {
		source := ArtifactSource{Repository: "https://github.com/" + parts[0] + "/" + parts[1], CommitSHA: strings.ToLower(parts[3])}
		if _, err := source.ArchiveURL(); err != nil {
			return ArtifactSource{}, err
		}
		return source, nil
	}
	return ArtifactSource{}, fmt.Errorf("%w: public GitHub URL and exact commit SHA required", ErrInvalid)
}

func rejectEscalation(definition ToolDefinition, args map[string]any) error {
	allowedEffects := map[string]bool{}
	allowedTools := map[string]bool{}
	for _, tool := range definition.Tools {
		allowedEffects[string(tool.Effect)] = true
		allowedTools[tool.Name] = true
	}
	for _, effect := range argStrings(args, "effects") {
		if !allowedEffects[effect] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	for _, name := range argStrings(args, "tools") {
		if !allowedTools[name] {
			return fmt.Errorf("%w: effect escalation", ErrUnauthorized)
		}
	}
	if n := argInt(args, "cpu_millis"); n > 0 && n > definition.Execution.CPUMillis {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "memory_mib"); n > 0 && n > definition.Execution.MemoryMiB {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "timeout_seconds"); n > 0 && n > definition.Execution.TimeoutSeconds {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "max_pids"); n > 0 && n > definition.Execution.MaxPIDs {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	if n := argInt(args, "output_bytes"); n > 0 && n > definition.Execution.OutputBytes {
		return fmt.Errorf("%w: budget escalation", ErrUnauthorized)
	}
	return nil
}

func credentialHints(definition ToolDefinition) []CredentialHint {
	hints := make([]CredentialHint, 0, len(definition.Credentials))
	for _, input := range definition.Credentials {
		if !input.Required {
			continue
		}
		kind := "secret"
		if strings.Contains(input.Name, "OAUTH") {
			kind = "oauth"
		}
		hints = append(hints, CredentialHint{Name: input.Name, Type: kind, Hint: "protected loopback form"})
	}
	return hints
}

func credentialNames(onboarding Onboarding) []string {
	names := make([]string, 0, len(onboarding.Required))
	for _, hint := range onboarding.Required {
		names = append(names, hint.Name)
	}
	return names
}

func toolNames(definition ToolDefinition) []string {
	names := make([]string, 0, len(definition.Tools))
	for _, tool := range definition.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func effectNames(definition ToolDefinition) []string {
	seen := map[string]bool{}
	effects := make([]string, 0)
	for _, tool := range definition.Tools {
		name := string(tool.Effect)
		if !seen[name] {
			seen[name] = true
			effects = append(effects, name)
		}
	}
	return effects
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok || value == nil {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func argStrings(args map[string]any, key string) []string {
	if args == nil {
		return nil
	}
	value, ok := args[key]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, _ := item.(string)
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func argInt(args map[string]any, key string) int {
	if args == nil {
		return 0
	}
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func randomNonce() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(raw)
}
