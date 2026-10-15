package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/letya999/hermes-hub/internal/identity"
	"time"
)

func DefinitionDigest(definition ToolDefinition) string {
	body, err := json.Marshal(definition)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ConfirmationDigest is the canonical digest the CLI prints for review and the
// store re-derives at admission. It covers the record with the confirmation
// field removed, so a stamped record digests identically to its reviewed form.
func ConfirmationDigest(record any) string {
	body, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	var shadow map[string]json.RawMessage
	if err := json.Unmarshal(body, &shadow); err != nil {
		return ""
	}
	delete(shadow, "confirmation")
	canonical, err := json.Marshal(shadow)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Confirm stamps issuer's confirmation onto a policy, profile, or grant
// pointer. An empty issuer defaults to the record's own issuer, which is also
// what cross-field identity validation enforces at admission.
func Confirm(record any, issuer string, now time.Time) error {
	slot, recordIssuer := confirmationTarget(record)
	if slot == nil {
		return fmt.Errorf("%w: unsupported confirmation record", ErrInvalid)
	}
	if issuer == "" {
		issuer = recordIssuer
	}
	*slot = nil
	confirmation, err := newConfirmation(record, issuer, now)
	if err != nil {
		return err
	}
	*slot = &confirmation
	return nil
}

func confirmationTarget(record any) (**Confirmation, string) {
	switch r := record.(type) {
	case *CapabilityPolicy:
		return &r.Confirmation, r.IssuedBy
	case *CapabilityProfile:
		return &r.Confirmation, r.IssuedBy
	case *Grant:
		return &r.Confirmation, r.IssuedBy
	case *ToolGrant:
		return &r.Confirmation, r.GrantedBy
	}
	return nil, ""
}

func newConfirmation(record any, issuer string, now time.Time) (Confirmation, error) {
	if !identity.ValidID(issuer) || issuer == "model" || issuer == "hermes" {
		return Confirmation{}, fmt.Errorf("%w: confirmation issuer", ErrUnauthorized)
	}
	digest := ConfirmationDigest(record)
	if digest == "" {
		return Confirmation{}, fmt.Errorf("%w: confirmation digest", ErrInvalid)
	}
	return Confirmation{Digest: digest, IssuedBy: issuer, ConfirmedAt: now.UTC()}, nil
}
