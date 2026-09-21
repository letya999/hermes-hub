// Package v1 contains the dependency-light, versioned ToolHub integration DTOs.
package v1

import (
	"time"

	"github.com/letya999/credential-broker/contract"
	"github.com/letya999/credential-broker/identity"
)

type CreateRequest struct {
	ContractID         string `json:"contract_id"`
	ContractRevision   int    `json:"contract_revision"`
	ConnectionID       string `json:"connection_id"`
	OnboardingID       string `json:"onboarding_id"`
	IdempotencyKey     string `json:"idempotency_key"`
	OwnerKind          string `json:"owner_kind"` // user or context
	ExternalAlias      string `json:"external_alias,omitempty"`
	RotateCredentialID string `json:"rotate_credential_id,omitempty"`
}
type Request struct {
	ID               string    `json:"id"`
	ContractID       string    `json:"contract_id"`
	ContractRevision int       `json:"contract_revision"`
	ConnectionID     string    `json:"connection_id"`
	OnboardingID     string    `json:"onboarding_id"`
	Status           string    `json:"status"`
	CredentialID     string    `json:"credential_id,omitempty"`
	Revision         uint64    `json:"revision,omitempty"`
	AuthorizationURL string    `json:"authorization_url"`
	ExpiresAt        time.Time `json:"expires_at"`
}
type Approve struct {
	Code string `json:"code"`
}
type GrantRequest struct {
	ContractID       string `json:"contract_id"`
	ContractRevision int    `json:"contract_revision"`
	PrincipalID      string `json:"principal_id"`
	ContextID        string `json:"context_id"`
	RuntimeID        string `json:"runtime_id"`
	BindingID        string `json:"binding_id"`
	WorkloadID       string `json:"workload_id"`
	Execution        string `json:"execution"` // dedicated or shared
	IdempotencyKey   string `json:"idempotency_key"`
}
type Grant struct {
	ID               string `json:"id"`
	CredentialID     string `json:"credential_id"`
	PrincipalID      string `json:"principal_id"`
	ContextID        string `json:"context_id"`
	RuntimeID        string `json:"runtime_id"`
	BindingID        string `json:"binding_id"`
	WorkloadID       string `json:"workload_id"`
	Execution        string `json:"execution"`
	ContractID       string `json:"contract_id"`
	ContractRevision int    `json:"contract_revision"`
	PolicyVersion    string `json:"policy_version"`
	Active           bool   `json:"active"`
}
type AcquireLease struct {
	GrantID    string `json:"grant_id"`
	TTLSeconds int    `json:"ttl_seconds"`
}
type Lease struct {
	ID           string              `json:"id"`
	GrantID      string              `json:"grant_id"`
	CredentialID string              `json:"credential_id"`
	Revision     uint64              `json:"revision"`
	ExpiresAt    time.Time           `json:"expires_at"`
	Deliveries   []contract.Delivery `json:"deliveries,omitempty"`
	State        *contract.State     `json:"state,omitempty"`
	Routes       []string            `json:"routes,omitempty"`
}
type Credential struct {
	ID           string `json:"id"`
	OwnerKind    string `json:"owner_kind"`
	PrincipalID  string `json:"principal_id"`
	ContextID    string `json:"context_id"`
	ConnectionID string `json:"connection_id"`
	Revision     uint64 `json:"revision"`
	Status       string `json:"status"`
	Provider     string `json:"provider"`
}
type Event struct {
	Sequence      uint64    `json:"sequence"`
	At            time.Time `json:"at"`
	Kind          string    `json:"kind"`
	PrincipalID   string    `json:"principal_id,omitempty"`
	ContextID     string    `json:"context_id,omitempty"`
	RequestID     string    `json:"request_id,omitempty"`
	ConnectionID  string    `json:"connection_id,omitempty"`
	OnboardingID  string    `json:"onboarding_id,omitempty"`
	CredentialID  string    `json:"credential_id,omitempty"`
	GrantID       string    `json:"grant_id,omitempty"`
	LeaseID       string    `json:"lease_id,omitempty"`
	BindingID     string    `json:"binding_id,omitempty"`
	WorkloadID    string    `json:"workload_id,omitempty"`
	PolicyVersion string    `json:"policy_version,omitempty"`
}
type Events struct {
	Events     []Event `json:"events"`
	NextCursor uint64  `json:"next_cursor"`
}
type Error struct {
	Code string `json:"code"`
}

// Runtime materialization contains plaintext env values and MUST only go to the
// authenticated runtime adapter. Never send this DTO through Hermes or ToolHub jobs.
type Materialized struct {
	LeaseID   string            `json:"lease_id"`
	ExpiresAt time.Time         `json:"expires_at"`
	Env       map[string]string `json:"env,omitempty"`
	Mounts    []Mount           `json:"mounts,omitempty"`
}
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only"`
}
type RuntimeRelease struct {
	Checkpoint bool `json:"checkpoint"`
	Quiesced   bool `json:"quiesced"`
}
type SessionView struct {
	RequestID string
	Title     string
	Status    string
	Code      string
	Approved  bool
	Fields    []contract.Field
	OAuth     bool
}
type RuntimeIdentity = identity.Actor
