package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
)

// HumanConfirmation travels only between the verified channel and ToolHub.
type HumanConfirmation struct {
	OnboardingID string            `json:"onboarding_id"`
	Actor        identity.Envelope `json:"actor"`
	Nonce        string            `json:"nonce"`
	Revision     uint64            `json:"revision"`
	ActionDigest string            `json:"action_digest"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

type HumanConsent struct {
	ActorID      string    `json:"actor_id"`
	ActionDigest string    `json:"action_digest"`
	Revision     uint64    `json:"revision"`
	ConfirmedAt  time.Time `json:"confirmed_at"`
	// Source names which owner signal counted as consent: a protected-link
	// click ("confirmation"), a credential submission, an OAuth grant at the
	// provider, or a credential-free install request. Empty predates the
	// split and means a clicked confirmation.
	Source string `json:"source,omitempty"`
}

// ConfirmationActionDigest binds consent to the reviewed action, excluding link
// tokens and incidental observations. Revision and expiry are checked separately.
func ConfirmationActionDigest(o Onboarding) string {
	body, _ := json.Marshal(struct {
		ID, Principal, Context, Runtime, Policy             string
		Definition, Version, Review, Connection, Credential string
		BrokerCredential, BrokerContract, Recipe            string
		BrokerContractRevision                              int
		Permissions, Effects                                []string
		PendingWrites, WritesEnabled                        bool
	}{o.OnboardingID, o.PrincipalID, o.ContextID, o.RuntimeID, o.PolicyVersion,
		o.DefinitionID, o.DefinitionVersion, o.ReviewDigest, o.ConnectionID, o.CredentialRefID,
		o.BrokerCredentialID, o.BrokerContractID, o.RecipeDigest, o.BrokerContractRevision,
		o.Permissions, o.Effects, o.PendingWrites, o.WritesEnabled})
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}
