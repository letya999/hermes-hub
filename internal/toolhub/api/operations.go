package api

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

const (
	OpIntentInstall     = "install"
	OpIntentEnable      = "enable"
	OpIntentDisable     = "disable"
	OpIntentReauth      = "reauth"
	OpIntentAllowWrites = "allow_writes"
	OpIntentRemove      = "remove"
)

const (
	OpStateQueued          = "queued"
	OpStateAwaitingUser    = "awaiting_user"
	OpStateAwaitingConfirm = "awaiting_confirm"
	OpStateRunning         = "running"
	OpStateDone            = "done"
	OpStateFailed          = "failed"
)

const (
	OpStepPending = "pending"
	OpStepDone    = "done"
)

// Canonical connector operation step names. The install intent persists this
// frozen order at creation so the journal runner can resume an in-flight
// record: every step is idempotent and keyed by name.
const (
	OpStepResolve            = "resolve"
	OpStepFetchArtifact      = "fetch_artifact"
	OpStepCollectCredentials = "collect_credentials"
	OpStepAdmitProbe         = "admit_probe"
	OpStepBind               = "bind"
	OpStepProject            = "project"
	OpStepNotify             = "notify"
	OpStepApply              = "apply"
	OpStepConfirm            = "confirm"
	OpStepCutAuthorization   = "cut_authorization"
	OpStepPurge              = "purge"
	OpStepVerifyZero         = "verify_zero"
)

// Stable typed error codes for connector_* results — the control-flow
// contract. Results never branch on prose.
const (
	OpCodeAmbiguousTarget             = "ambiguous_target"
	OpCodeAmbiguousEntrypoint         = "ambiguous_entrypoint"
	OpCodeAmbiguousCredentialContract = "ambiguous_credential_contract"
	OpCodeUnsupportedAuth             = "unsupported_auth"
	OpCodeProbeFailed                 = "probe_failed"
	OpCodeCleanupIncomplete           = "cleanup_incomplete"
	OpCodeNotPermitted                = "not_permitted"
	OpCodeQuota                       = "quota"
)

type OperationStep struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// OperationResult is the uniform connector_* tool result — the only shape
// the agent ever sees. No nonces, URLs, tokens or prose instructions.
type OperationResult struct {
	OperationID string               `json:"operation_id,omitempty"`
	State       string               `json:"state"`
	Code        string               `json:"code,omitempty"`
	Summary     string               `json:"summary"`
	LinkSent    bool                 `json:"link_sent,omitempty"`
	Candidates  []OperationCandidate `json:"candidates,omitempty"`
	Finalizers  []Finalizer          `json:"finalizers,omitempty"`
	Receipt     string               `json:"receipt,omitempty"`
}

// OperationCandidate is one selector match shown on ambiguity.
type OperationCandidate struct {
	ID                   string    `json:"id"`
	Source               string    `json:"source,omitempty"`
	Kind                 string    `json:"kind,omitempty"`
	DefinitionID         string    `json:"definition_id,omitempty"`
	ConnectionID         string    `json:"connection_id,omitempty"`
	Owner                *OwnerRef `json:"owner,omitempty"`
	Transport            string    `json:"transport,omitempty"`
	ConnectionStatus     Status    `json:"connection_status,omitempty"`
	CredentialVisibility string    `json:"credential_visibility,omitempty"`
	CredentialCheckedAt  time.Time `json:"credential_checked_at,omitempty"`
	HealthCause          string    `json:"health_cause,omitempty"`
	OnboardingPhase      string    `json:"onboarding_phase,omitempty"`
	// Badge marks candidates the caller can use but does not own — today the
	// only value is "team" for a member's team-owned connection.
	Badge string `json:"badge,omitempty"`
}

type Operation struct {
	Schema        int             `json:"schema"`
	OperationID   string          `json:"operation_id"`
	Intent        string          `json:"intent"`
	Target        string          `json:"target"`
	PrincipalID   string          `json:"principal_id"`
	ContextID     string          `json:"context_id"`
	RuntimeID     string          `json:"runtime_id"`
	PolicyVersion string          `json:"policy_version"`
	RequestKey    string          `json:"request_key"`
	State         string          `json:"state"`
	Steps         []OperationStep `json:"steps,omitempty"`
	OnboardingID  string          `json:"onboarding_id,omitempty"`
	SupersededBy  string          `json:"superseded_by,omitempty"`
	ErrorCode     string          `json:"error_code,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at,omitempty"`
}

// RefreshLease is the durable (connection, provider) refresh claim: one
// caller holds it while exchanging a rotating refresh token; concurrent
// callers join on the holder's result instead of double-spending.
type RefreshLease struct {
	ConnectionID string    `json:"connection_id"`
	Provider     string    `json:"provider"`
	Holder       string    `json:"holder"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func (l RefreshLease) Validate() error {
	if !identity.ValidID(l.ConnectionID) || l.Provider == "" || l.Holder == "" || l.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: refresh lease", ErrInvalid)
	}
	return nil
}

func (o Operation) Validate() error {
	if !identity.ValidID(o.PrincipalID) {
		return fmt.Errorf("%w: operation principal", ErrInvalid)
	}
	if o.Target == "" || !slices.Contains([]string{OpIntentInstall, OpIntentEnable, OpIntentDisable, OpIntentReauth, OpIntentAllowWrites, OpIntentRemove}, o.Intent) {
		return fmt.Errorf("%w: operation intent or target", ErrInvalid)
	}
	switch o.State {
	case OpStateQueued, OpStateAwaitingUser, OpStateAwaitingConfirm, OpStateRunning, OpStateDone, OpStateFailed:
	default:
		return fmt.Errorf("%w: operation state", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, step := range o.Steps {
		if seen[step.Name] || step.State != OpStepPending && step.State != OpStepDone {
			return fmt.Errorf("%w: operation step", ErrInvalid)
		}
		seen[step.Name] = true
	}
	switch o.ErrorCode {
	case "", OpCodeAmbiguousTarget, OpCodeAmbiguousEntrypoint, OpCodeAmbiguousCredentialContract, OpCodeUnsupportedAuth, OpCodeProbeFailed, OpCodeCleanupIncomplete, OpCodeNotPermitted, OpCodeQuota:
	default:
		return fmt.Errorf("%w: operation error code", ErrInvalid)
	}
	return nil
}

// OperationRequestKey derives the durable idempotency key from
// (principal, intent, normalized target) — the same call twice resolves the
// same operation.
func OperationRequestKey(auth identity.Envelope, intent, target string) string {
	normalized := strings.ToLower(strings.TrimSpace(target))
	return DeterministicID("opkey", auth.PrincipalID, intent, normalized)
}
