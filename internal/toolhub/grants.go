package toolhub

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

type GrantKind string

const (
	GrantCatalogDefault   GrantKind = "catalog-default"
	GrantDefinition       GrantKind = "definition"
	GrantSelfInstall      GrantKind = "self-install"
	GrantSharedCredential GrantKind = "shared-credential"
	PublicationCatalog    string    = "catalog"
	PublicationUser       string    = "user"
	OnboardingCatalog     string    = "catalog"
	OnboardingSelfInstall string    = "self-install"
	PhasePreparing        string    = "preparing"
	PhaseReview           string    = "review"
	PhaseAwaitingCreds    string    = "awaiting-credentials"
	PhaseAwaitingConfirm  string    = "awaiting-confirm"
	PhaseAwaitingOAuth    string    = "awaiting-oauth"
	PhaseConfirmed        string    = "confirmed"
	PhaseEnabled          string    = "enabled"
	PhaseDisabled         string    = "disabled"
	PhaseRevoked          string    = "revoked"
	PhaseRemoved          string    = "removed"
	PhaseFailed           string    = "failed"
)

var ControlOperations = []string{"discover", "prepare_source", "status", "required_credentials", "confirm", "enable", "rotate", "disable", "revoke", "remove"}

type Grant struct {
	Schema            int       `json:"schema"`
	GrantID           string    `json:"grant_id"`
	Kind              GrantKind `json:"kind"`
	PrincipalID       string    `json:"principal_id"`
	DefinitionID      string    `json:"definition_id,omitempty"`
	DefinitionVersion string    `json:"definition_version,omitempty"`
	IssuedBy          string    `json:"issued_by"`
	Status            Status    `json:"status"`
	Revision          uint64    `json:"revision"`
}

type DefinitionPublication struct {
	DefinitionID     string `json:"definition_id"`
	Version          string `json:"version"`
	Visibility       string `json:"visibility"`
	OwnerPrincipalID string `json:"owner_principal_id,omitempty"`
}

type SharedCredentialPolicy struct {
	Schema       int      `json:"schema"`
	PolicyID     string   `json:"policy_id"`
	DefinitionID string   `json:"definition_id"`
	Locator      string   `json:"locator"`
	StoreOwner   string   `json:"store_owner"`
	Principals   []string `json:"principals"`
	IssuedBy     string   `json:"issued_by"`
	Status       Status   `json:"status"`
}

type CredentialHint struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Hint           string `json:"hint,omitempty"`
	Secret         bool   `json:"secret,omitempty"`
	Delivery       string `json:"delivery,omitempty"`
	Target         string `json:"target,omitempty"`
	Alternative    string `json:"alternative,omitempty"`
	AlternativeURL string `json:"alternative_url,omitempty"`
}

type Onboarding struct {
	Schema                   int               `json:"schema"`
	OnboardingID             string            `json:"onboarding_id"`
	PrincipalID              string            `json:"principal_id"`
	ContextID                string            `json:"context_id"`
	RuntimeID                string            `json:"runtime_id"`
	PolicyVersion            string            `json:"policy_version"`
	Mode                     string            `json:"mode"`
	SourceURL                string            `json:"source_url,omitempty"`
	Subfolder                string            `json:"subfolder,omitempty"`
	CommitSHA                string            `json:"commit_sha,omitempty"`
	DefinitionID             string            `json:"definition_id,omitempty"`
	DefinitionVersion        string            `json:"definition_version,omitempty"`
	Phase                    string            `json:"phase"`
	BindingID                string            `json:"binding_id,omitempty"`
	ConnectionID             string            `json:"connection_id,omitempty"`
	CredentialRefID          string            `json:"credential_ref,omitempty"`
	ReviewDigest             string            `json:"review_digest,omitempty"`
	Required                 []CredentialHint  `json:"required,omitempty"`
	Permissions              []string          `json:"permissions,omitempty"`
	Effects                  []string          `json:"effects,omitempty"`
	ConfirmationNonce        string            `json:"confirmation_nonce,omitempty"`
	ConfirmationExpires      time.Time         `json:"confirmation_expires,omitempty"`
	ConfirmationUsed         bool              `json:"confirmation_used,omitempty"`
	IdempotencyKey           string            `json:"idempotency_key,omitempty"`
	FormNonce                string            `json:"form_nonce,omitempty"`
	FormExpires              time.Time         `json:"form_expires,omitempty"`
	Locator                  string            `json:"locator,omitempty"`
	CredentialOwner          string            `json:"credential_owner,omitempty"`
	BrokerRequestID          string            `json:"broker_request_id,omitempty"`
	BrokerAttempts           int               `json:"broker_attempts,omitempty"`
	BrokerCredentialID       string            `json:"broker_credential_id,omitempty"`
	BrokerRotateCredentialID string            `json:"broker_rotate_credential_id,omitempty"`
	BrokerGrantID            string            `json:"broker_grant_id,omitempty"`
	BrokerContractID         string            `json:"broker_contract_id,omitempty"`
	BrokerContractRevision   int               `json:"broker_contract_revision,omitempty"`
	BrokerAuthorizationURL   string            `json:"broker_authorization_url,omitempty"`
	ProviderAuthorizationURL string            `json:"provider_authorization_url,omitempty"`
	Recipe                   *RecipeResolution `json:"recipe,omitempty"`
	Revision                 uint64            `json:"revision"`
	CreatedAt                time.Time         `json:"created_at"`
	Definition               *ToolDefinition   `json:"definition,omitempty"`
}

func OperatorGrant(kind GrantKind, principal, definitionID, version string) Grant {
	return Grant{
		Schema: SchemaVersion, GrantID: deterministicID("grant", string(kind), principal, definitionID, version),
		Kind: kind, PrincipalID: principal, DefinitionID: definitionID, DefinitionVersion: version,
		IssuedBy: "operator", Status: ActiveStatus, Revision: 1,
	}
}

func (g Grant) Validate() error {
	if g.Schema != SchemaVersion || !identity.ValidID(g.GrantID) || !identity.ValidID(g.PrincipalID) || !identity.ValidID(g.IssuedBy) || g.Revision == 0 {
		return fmt.Errorf("%w: grant identity", ErrInvalid)
	}
	if g.IssuedBy == "model" || g.IssuedBy == "hermes" {
		return fmt.Errorf("%w: model cannot issue a grant", ErrUnauthorized)
	}
	if g.Status != ActiveStatus && g.Status != DisabledStatus && g.Status != RevokedStatus {
		return fmt.Errorf("%w: grant status", ErrInvalid)
	}
	switch g.Kind {
	case GrantCatalogDefault, GrantSelfInstall:
		if g.DefinitionID != "" || g.DefinitionVersion != "" {
			return fmt.Errorf("%w: %s grant cannot name a definition", ErrInvalid, g.Kind)
		}
	case GrantDefinition:
		if !identity.ValidID(g.DefinitionID) || !versionPattern.MatchString(g.DefinitionVersion) {
			return fmt.Errorf("%w: definition grant", ErrInvalid)
		}
	case GrantSharedCredential:
		if !identity.ValidID(g.DefinitionID) {
			return fmt.Errorf("%w: shared-credential grant", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: grant kind", ErrInvalid)
	}
	return nil
}

func (p DefinitionPublication) Validate() error {
	if !identity.ValidID(p.DefinitionID) || !versionPattern.MatchString(p.Version) {
		return fmt.Errorf("%w: publication identity", ErrInvalid)
	}
	switch p.Visibility {
	case PublicationCatalog:
		return nil
	case PublicationUser:
		if !identity.ValidID(p.OwnerPrincipalID) {
			return fmt.Errorf("%w: user publication owner", ErrInvalid)
		}
		return nil
	default:
		return fmt.Errorf("%w: publication visibility", ErrInvalid)
	}
}

func (p SharedCredentialPolicy) Validate() error {
	if p.Schema != SchemaVersion || !identity.ValidID(p.PolicyID) || !identity.ValidID(p.DefinitionID) || !identity.ValidID(p.StoreOwner) || !identity.ValidID(p.IssuedBy) {
		return fmt.Errorf("%w: shared credential policy identity", ErrInvalid)
	}
	if p.IssuedBy == "model" || p.IssuedBy == "hermes" {
		return fmt.Errorf("%w: model cannot issue a shared credential policy", ErrUnauthorized)
	}
	if p.Locator == "" || strings.ContainsAny(p.Locator, "\r\n=") || len(p.Locator) > 256 {
		return fmt.Errorf("%w: shared locator", ErrInvalid)
	}
	if p.Status != ActiveStatus && p.Status != DisabledStatus && p.Status != RevokedStatus {
		return fmt.Errorf("%w: shared policy status", ErrInvalid)
	}
	if len(p.Principals) == 0 || len(p.Principals) > 256 {
		return fmt.Errorf("%w: shared policy principals", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, principal := range p.Principals {
		if !identity.ValidID(principal) || seen[principal] {
			return fmt.Errorf("%w: shared policy principal", ErrInvalid)
		}
		seen[principal] = true
	}
	return nil
}

func (o Onboarding) Validate() error {
	if o.Schema != SchemaVersion || !identity.ValidID(o.OnboardingID) || !identity.ValidID(o.PrincipalID) || !identity.ValidID(o.ContextID) || !identity.ValidID(o.RuntimeID) || !identity.ValidID(o.PolicyVersion) || o.Revision == 0 {
		return fmt.Errorf("%w: onboarding identity", ErrInvalid)
	}
	if o.Mode != OnboardingCatalog && o.Mode != OnboardingSelfInstall {
		return fmt.Errorf("%w: onboarding mode", ErrInvalid)
	}
	switch o.Phase {
	case PhasePreparing, PhaseReview, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth, PhaseConfirmed, PhaseEnabled, PhaseDisabled, PhaseRevoked, PhaseRemoved, PhaseFailed:
	default:
		return fmt.Errorf("%w: onboarding phase", ErrInvalid)
	}
	if o.DefinitionID != "" && !identity.ValidID(o.DefinitionID) {
		return fmt.Errorf("%w: onboarding definition", ErrInvalid)
	}
	if o.DefinitionVersion != "" && !versionPattern.MatchString(o.DefinitionVersion) {
		return fmt.Errorf("%w: onboarding version", ErrInvalid)
	}
	return nil
}

func (s *Store) PutGrant(grant Grant) error {
	if err := grant.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if existing, ok := s.grants[grant.GrantID]; ok && !recordsEqual(existing, grant) && existing.Revision == grant.Revision {
		s.mu.Unlock()
		return fmt.Errorf("%w: grant is immutable within a revision", ErrConflict)
	}
	s.grants[grant.GrantID] = grant
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PutPublication(pub DefinitionPublication) error {
	if err := pub.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if !s.hasDefinitionID(pub.DefinitionID) {
		s.mu.Unlock()
		return fmt.Errorf("%w: publication definition", ErrNotFound)
	}
	s.publications[definitionKey(pub.DefinitionID, pub.Version)] = pub
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PutSharedCredentialPolicy(policy SharedCredentialPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.sharedPolicies[policy.PolicyID] = policy
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PutOnboarding(onboarding Onboarding) error {
	if err := onboarding.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	s.onboardings[onboarding.OnboardingID] = onboarding
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) PromoteToCatalog(definitionID, version, operator string) error {
	if !identity.ValidID(operator) || operator == "model" || operator == "hermes" {
		return fmt.Errorf("%w: catalog promotion issuer", ErrUnauthorized)
	}
	s.mu.Lock()
	key := definitionKey(definitionID, version)
	if _, ok := s.definitions[key]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("%w: definition", ErrNotFound)
	}
	pub := s.publications[key]
	pub.DefinitionID = definitionID
	pub.Version = version
	pub.Visibility = PublicationCatalog
	if err := pub.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.publications[key] = pub
	s.mu.Unlock()
	return s.persist()
}

func (s *Store) publicationLocked(definition ToolDefinition) DefinitionPublication {
	if pub, ok := s.publications[definitionKey(definition.DefinitionID, definition.Version)]; ok {
		return pub
	}
	return DefinitionPublication{DefinitionID: definition.DefinitionID, Version: definition.Version, Visibility: PublicationCatalog}
}

func (s *Store) visibleDefinitionLocked(auth identity.Envelope, definition ToolDefinition) bool {
	pub := s.publicationLocked(definition)
	if pub.Visibility == PublicationUser {
		return pub.OwnerPrincipalID == auth.PrincipalID
	}
	if len(s.grants) == 0 {
		return true
	}
	return s.hasCatalogAccessLocked(auth, definition.DefinitionID, definition.Version)
}

func (s *Store) hasCatalogAccessLocked(auth identity.Envelope, definitionID, version string) bool {
	for _, grant := range s.grants {
		if grant.Status != ActiveStatus || grant.PrincipalID != auth.PrincipalID {
			continue
		}
		switch grant.Kind {
		case GrantCatalogDefault:
			return true
		case GrantDefinition:
			if grant.DefinitionID == definitionID && grant.DefinitionVersion == version {
				return true
			}
		}
	}
	return false
}

func (s *Store) selfInstallDeniedLocked(auth identity.Envelope) bool {
	for _, grant := range s.grants {
		if grant.Kind == GrantSelfInstall && grant.PrincipalID == auth.PrincipalID {
			return grant.Status != ActiveStatus
		}
	}
	return false
}

func (s *Store) sharedPolicyLocked(auth identity.Envelope, definitionID string) *SharedCredentialPolicy {
	for _, policy := range s.sharedPolicies {
		if policy.Status != ActiveStatus || policy.DefinitionID != definitionID {
			continue
		}
		if slices.Contains(policy.Principals, auth.PrincipalID) {
			copy := policy
			return &copy
		}
	}
	return nil
}

func (s *Store) RequireCatalogAccess(auth identity.Envelope, definition ToolDefinition) error {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	pub := s.publicationLocked(definition)
	if pub.Visibility == PublicationUser {
		if pub.OwnerPrincipalID != auth.PrincipalID {
			return fmt.Errorf("%w: user definition owner", ErrUnauthorized)
		}
		return nil
	}
	if !s.hasCatalogAccessLocked(auth, definition.DefinitionID, definition.Version) {
		return fmt.Errorf("%w: catalog grant", ErrUnauthorized)
	}
	return nil
}

func (s *Store) RequireSelfInstall(auth identity.Envelope) error {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// Self-install is allowed for every principal by default; an operator can
	// still withdraw it per principal with a disabled or revoked grant.
	if s.selfInstallDeniedLocked(auth) {
		return fmt.Errorf("%w: self-install grant", ErrUnauthorized)
	}
	return nil
}

func (s *Store) OnboardingFor(auth identity.Envelope, id string) (Onboarding, error) {
	if err := auth.Validate(auth.PrincipalID, auth.ContextID, auth.RuntimeID, auth.PolicyVersion); err != nil {
		return Onboarding{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	onboarding, ok := s.onboardings[id]
	if !ok {
		return Onboarding{}, fmt.Errorf("%w: onboarding", ErrNotFound)
	}
	if onboarding.PrincipalID != auth.PrincipalID || onboarding.ContextID != auth.ContextID || onboarding.RuntimeID != auth.RuntimeID || onboarding.PolicyVersion != auth.PolicyVersion {
		return Onboarding{}, fmt.Errorf("%w: onboarding owner", ErrUnauthorized)
	}
	return onboarding, nil
}

func (s *Store) FindOnboardingByKey(auth identity.Envelope, key string) (Onboarding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, onboarding := range s.onboardings {
		if onboarding.PrincipalID == auth.PrincipalID && onboarding.ContextID == auth.ContextID && onboarding.RuntimeID == auth.RuntimeID && onboarding.PolicyVersion == auth.PolicyVersion && onboarding.IdempotencyKey == key && key != "" {
			return onboarding, true
		}
	}
	return Onboarding{}, false
}
