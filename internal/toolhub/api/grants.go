package api

import (
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"slices"
	"strings"
	"time"
)

type GrantKind string

const (
	GrantCatalogDefault   GrantKind = "catalog-default"
	GrantDefinition       GrantKind = "definition"
	GrantSelfInstall      GrantKind = "self-install"
	GrantSharedCredential GrantKind = "shared-credential"
	GrantControlOperation GrantKind = "control-operation"
	PublicationCatalog    string    = "catalog"
	PublicationUser       string    = "user"
	OnboardingCatalog     string    = "catalog"
	OnboardingSelfInstall string    = "self-install"
	OnboardingRemote      string    = "remote-mcp"
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
	// PhaseCleanupIncomplete marks a remove whose cleanup did not converge:
	// no removed tombstone until every step succeeded, and a bare retry
	// resumes the remaining work without another confirmation.
	PhaseCleanupIncomplete string = "cleanup_incomplete"
	PhaseFailed            string = "failed"
)

var ControlOperations = []string{"discover", "prepare_source", "status", "required_credentials", "confirm", "enable", "rotate", "disable", "revoke", "remove", "diagnostics", "grant_request", "install_shared"}

type Grant struct {
	Schema            int           `json:"schema"`
	GrantID           string        `json:"grant_id"`
	Kind              GrantKind     `json:"kind"`
	PrincipalID       string        `json:"principal_id"`
	DefinitionID      string        `json:"definition_id,omitempty"`
	DefinitionVersion string        `json:"definition_version,omitempty"`
	Operation         string        `json:"operation,omitempty"`
	IssuedBy          string        `json:"issued_by"`
	Status            Status        `json:"status"`
	Revision          uint64        `json:"revision"`
	Confirmation      *Confirmation `json:"confirmation,omitempty"`
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
	Name   string `json:"name"`
	Type   string `json:"type"`
	Hint   string `json:"hint,omitempty"`
	Secret bool   `json:"secret,omitempty"`
	// AlternativeGroup marks OR groups from one auth sentence. Zero means the
	// field is independently required. A positive group is accepted only when
	// exactly one complete group is filled.
	AlternativeGroup int    `json:"alternative_group,omitempty"`
	Delivery         string `json:"delivery,omitempty"`
	Target           string `json:"target,omitempty"`
	Alternative      string `json:"alternative,omitempty"`
	AlternativeURL   string `json:"alternative_url,omitempty"`
	// Source names the metadata file the field was discovered from when the
	// hint came from a connection recipe (server.json, .env.example, ...).
	Source string `json:"source,omitempty"`
}

type Onboarding struct {
	Schema            int              `json:"schema"`
	OnboardingID      string           `json:"onboarding_id"`
	PrincipalID       string           `json:"principal_id"`
	ContextID         string           `json:"context_id"`
	RuntimeID         string           `json:"runtime_id"`
	PolicyVersion     string           `json:"policy_version"`
	Mode              string           `json:"mode"`
	SourceURL         string           `json:"source_url,omitempty"`
	Subfolder         string           `json:"subfolder,omitempty"`
	CommitSHA         string           `json:"commit_sha,omitempty"`
	DefinitionID      string           `json:"definition_id,omitempty"`
	DefinitionVersion string           `json:"definition_version,omitempty"`
	Phase             string           `json:"phase"`
	BindingID         string           `json:"binding_id,omitempty"`
	ConnectionID      string           `json:"connection_id,omitempty"`
	CredentialRefID   string           `json:"credential_ref,omitempty"`
	ReviewDigest      string           `json:"review_digest,omitempty"`
	Required          []CredentialHint `json:"required,omitempty"`
	// Optional lists discovered non-required inputs the user may fill in the
	// same protected form. They are shown to the user but never gate the
	// credential phase; empty submissions simply stay undelivered.
	Optional            []CredentialHint `json:"optional,omitempty"`
	Permissions         []string         `json:"permissions,omitempty"`
	Effects             []string         `json:"effects,omitempty"`
	ConfirmationNonce   string           `json:"confirmation_nonce,omitempty"`
	ConfirmationExpires time.Time        `json:"confirmation_expires,omitempty"`
	ConfirmationUsed    bool             `json:"confirmation_used,omitempty"`
	// RemoveNonce/RemoveExpires arm the destructive remove: the first call
	// previews and issues the nonce; only a follow-up remove carrying it
	// deletes credentials, workloads and state.
	RemoveNonce   string    `json:"remove_nonce,omitempty"`
	RemoveExpires time.Time `json:"remove_expires,omitempty"`
	// Finalizers is the convergent remove ladder: each named step is
	// idempotent and recorded so a retry resumes at the first incomplete
	// step and connector_status shows where a cleanup is stuck.
	Finalizers []Finalizer `json:"finalizers,omitempty"`
	// RemoveReceipt is the verified-zero summary emitted once every label
	// query across Docker, Broker and the filesystem reports no leftovers.
	RemoveReceipt string `json:"remove_receipt,omitempty"`
	// RemoveFootprint counts labelled resources found when the remove
	// started; the receipt reports what the ladder purged.
	RemoveFootprint          map[string]int `json:"remove_footprint,omitempty"`
	IdempotencyKey           string         `json:"idempotency_key,omitempty"`
	FormNonce                string         `json:"form_nonce,omitempty"`
	FormExpires              time.Time      `json:"form_expires,omitempty"`
	Locator                  string         `json:"locator,omitempty"`
	CredentialOwner          string         `json:"credential_owner,omitempty"`
	BrokerRequestID          string         `json:"broker_request_id,omitempty"`
	BrokerAttempts           int            `json:"broker_attempts,omitempty"`
	BrokerCredentialID       string         `json:"broker_credential_id,omitempty"`
	BrokerRotateCredentialID string         `json:"broker_rotate_credential_id,omitempty"`
	BrokerGrantID            string         `json:"broker_grant_id,omitempty"`
	BrokerContractID         string         `json:"broker_contract_id,omitempty"`
	BrokerContractRevision   int            `json:"broker_contract_revision,omitempty"`
	BrokerAuthorizationURL   string         `json:"broker_authorization_url,omitempty"`
	ProviderAuthorizationURL string         `json:"provider_authorization_url,omitempty"`
	// OAuthProvider names the broker slot that minted this onboarding's
	// credential. Set only for OAuth-managed connections so the call path
	// knows a rejected call may be refreshed and retried once.
	OAuthProvider string `json:"oauth_provider,omitempty"`
	// Device-flow session started from the /device page: the broker-side
	// pending entry is keyed by DeviceCode; user code and verification URI are
	// display data so the page can re-render after a reload.
	DeviceCode         string `json:"device_code,omitempty"`
	DeviceUserCode     string `json:"device_user_code,omitempty"`
	DeviceVerification string `json:"device_verification,omitempty"`
	SupersededBy       string `json:"superseded_by,omitempty"`
	// AdmissionPending means tools/list has not run because the server named
	// required secrets and exited first. The definition is a draft until the
	// protected form is submitted and the probe succeeds.
	AdmissionPending bool   `json:"admission_pending,omitempty"`
	Error            string `json:"error,omitempty"`
	// ErrorCode is the stable machine-readable companion of Error; it is set
	// from the failure cause via ErrorCode() and survives snapshot reloads.
	ErrorCode string `json:"error_code,omitempty"`
	// RecipeDigest references the content-addressed recipe stored under
	// snapshot recipes/<sha256>; the resolution itself is never embedded.
	RecipeDigest string            `json:"recipe_digest,omitempty"`
	Recipe       *RecipeResolution `json:"-"`
	// CredentialSources records every distinct metadata source the recipe
	// discovered credential declarations in. When several are present the
	// caller may ask the user which one to trust and replay prepare_source
	// with credential_source.
	CredentialSources []string `json:"credential_sources,omitempty"`
	// WritesEnabled means write-effect tools of this connector may project
	// and dispatch. Default off: connector_allow_writes(on) flips it only
	// through the protected confirm link.
	WritesEnabled bool `json:"writes_enabled,omitempty"`
	// PendingWrites marks a live connector whose confirm link currently arms
	// a write-enable instead of a first bind.
	PendingWrites bool            `json:"pending_writes,omitempty"`
	Revision      uint64          `json:"revision"`
	CreatedAt     time.Time       `json:"created_at"`
	Definition    *ToolDefinition `json:"definition,omitempty"`
}

func OperatorGrant(kind GrantKind, principal, definitionID, version string) Grant {
	return Grant{
		Schema: SchemaVersion, GrantID: DeterministicID("grant", string(kind), principal, definitionID, version),
		Kind: kind, PrincipalID: principal, DefinitionID: definitionID, DefinitionVersion: version,
		IssuedBy: "operator", Status: ActiveStatus, Revision: 1,
	}
}

func OperatorControlGrant(principal, operation string) Grant {
	grant := OperatorGrant(GrantControlOperation, principal, "", "")
	grant.Operation = operation
	grant.GrantID = DeterministicID("grant", string(grant.Kind), principal, operation)
	return grant
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
	if g.Kind != GrantControlOperation && g.Operation != "" {
		return fmt.Errorf("%w: operation on non-control grant", ErrInvalid)
	}
	switch g.Kind {
	case GrantControlOperation:
		if !ValidControlOperation(g.Operation) || g.DefinitionID != "" || g.DefinitionVersion != "" {
			return fmt.Errorf("%w: control operation grant", ErrInvalid)
		}
	case GrantCatalogDefault, GrantSelfInstall:
		if g.DefinitionID != "" || g.DefinitionVersion != "" {
			return fmt.Errorf("%w: %s grant cannot name a definition", ErrInvalid, g.Kind)
		}
	case GrantDefinition:
		if !identity.ValidID(g.DefinitionID) || !VersionPattern.MatchString(g.DefinitionVersion) {
			return fmt.Errorf("%w: definition grant", ErrInvalid)
		}
	case GrantSharedCredential:
		if !identity.ValidID(g.DefinitionID) {
			return fmt.Errorf("%w: shared-credential grant", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: grant kind", ErrInvalid)
	}
	if !validConfirmation(g.Confirmation, g, g.IssuedBy) {
		return fmt.Errorf("%w: unconfirmed grant", ErrUnauthorized)
	}
	return nil
}

func (p DefinitionPublication) Validate() error {
	if !identity.ValidID(p.DefinitionID) || !VersionPattern.MatchString(p.Version) {
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
	if o.Mode != OnboardingCatalog && o.Mode != OnboardingSelfInstall && o.Mode != OnboardingRemote {
		return fmt.Errorf("%w: onboarding mode", ErrInvalid)
	}
	switch o.Phase {
	case PhasePreparing, PhaseReview, PhaseAwaitingCreds, PhaseAwaitingConfirm, PhaseAwaitingOAuth, PhaseConfirmed, PhaseEnabled, PhaseDisabled, PhaseRevoked, PhaseRemoved, PhaseCleanupIncomplete, PhaseFailed:
	default:
		return fmt.Errorf("%w: onboarding phase", ErrInvalid)
	}
	if o.DefinitionID != "" && !identity.ValidID(o.DefinitionID) {
		return fmt.Errorf("%w: onboarding definition", ErrInvalid)
	}
	if o.DefinitionVersion != "" && !VersionPattern.MatchString(o.DefinitionVersion) {
		return fmt.Errorf("%w: onboarding version", ErrInvalid)
	}
	return nil
}

func ValidControlOperation(operation string) bool {
	return operation == "invoke" || slices.Contains(ControlOperations, operation)
}
