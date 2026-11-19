// Package identity defines trusted routing identifiers shared by the gateway and runtime.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const Schema = 1
const MaxProvenanceBytes = 256

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)
var scopePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Envelope struct {
	Schema             int    `json:"identity_schema"`
	PrincipalID        string `json:"principal_id"`
	ExternalIdentityID string `json:"external_identity_id"`
	ContextID          string `json:"context_id"`
	RuntimeID          string `json:"runtime_id"`
	ConversationID     string `json:"conversation_id"`
	DeliveryTargetID   string `json:"delivery_target_id"`
	PolicyVersion      string `json:"policy_version"`
	// Organization carries the principal's org scope for host-side policy
	// evaluation (tool governance org rules). Optional; empty is personal.
	Organization      string `json:"organization,omitempty"`
	CapabilityProfile string `json:"capability_profile,omitempty"`
	Environment       string `json:"environment,omitempty"`
	Generation        uint64 `json:"generation,omitempty"`
	// Scopes grants the bearer extra read-only surfaces (issue 141); only
	// "admin" is defined. Envelope-bearing endpoints ignore unknown scopes.
	Scopes []string `json:"scopes,omitempty"`
	// Issuer records which authority vouched for the principal at
	// ingress: "local" for channel-mapped traffic, the OIDC issuer for
	// SSO-linked accounts (issue #53). Provenance only — never a routing
	// key, never parsed for authorization.
	Issuer string `json:"issuer,omitempty"`
	// Subject is the issuer's opaque account id (OIDC `sub`). Empty for
	// channel-mapped principals.
	Subject string `json:"subject,omitempty"`
}

// HasScope reports whether the enrolled envelope carries the named scope.
func (e Envelope) HasScope(scope string) bool {
	return slices.Contains(e.Scopes, scope)
}

// Equal compares envelopes field-for-field. Envelope grew Scopes (a slice) in
// issue 141 and is no longer comparable with ==; call sites use Equal.
func (e Envelope) Equal(o Envelope) bool {
	return reflect.DeepEqual(e, o)
}

func ValidID(id string) bool { return idPattern.MatchString(id) }

func TelegramEnvelope(principal string, sender int64, runtime, policy string) Envelope {
	transportID := "telegram-" + strconv.FormatInt(sender, 10)
	return Envelope{Schema: Schema, PrincipalID: principal, ExternalIdentityID: transportID, ContextID: principal, RuntimeID: runtime, ConversationID: transportID, DeliveryTargetID: transportID, PolicyVersion: policy}
}

// SlackIdentity is the opaque verified Slack transport ID. Team and user
// values are lowercased; they are never derived from email or display name.
func SlackIdentity(teamID, userID string) string {
	team := strings.ToLower(strings.TrimSpace(teamID))
	user := strings.ToLower(strings.TrimSpace(userID))
	id := "slack-" + team + "-" + user
	if idPattern.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(team + "/" + user))
	return "slack-" + hex.EncodeToString(sum[:8])
}

func SlackEnvelope(principal, teamID, userID, runtime, policy string) Envelope {
	transportID := SlackIdentity(teamID, userID)
	return Envelope{Schema: Schema, PrincipalID: principal, ExternalIdentityID: transportID, ContextID: principal, RuntimeID: runtime, ConversationID: transportID, DeliveryTargetID: transportID, PolicyVersion: policy}
}

// MockEnvelope is the local test-bench channel identity: same binding
// discipline as the real channels, transport ids under the mock- prefix.
func MockEnvelope(principal string, chatID int64, runtime, policy string) Envelope {
	transportID := "mock-" + strconv.FormatInt(chatID, 10)
	return Envelope{Schema: Schema, PrincipalID: principal, ExternalIdentityID: transportID, ContextID: principal, RuntimeID: runtime, ConversationID: transportID, DeliveryTargetID: transportID, PolicyVersion: policy}
}

func (e Envelope) Validate(principal, context, runtime, policy string) error {
	if e.Schema != Schema {
		return errors.New("unsupported identity schema")
	}
	for _, id := range []string{e.PrincipalID, e.ExternalIdentityID, e.ContextID, e.RuntimeID, e.ConversationID, e.DeliveryTargetID, e.PolicyVersion} {
		if !ValidID(id) {
			return errors.New("invalid identity")
		}
	}
	if e.Organization != "" && !ValidID(e.Organization) {
		return errors.New("invalid identity")
	}
	if e.PrincipalID != principal || e.ContextID != context || e.RuntimeID != runtime || e.PolicyVersion != policy {
		return errors.New("identity binding mismatch")
	}
	if e.CapabilityProfile != "" || e.Environment != "" || e.Generation != 0 {
		if !ValidID(e.CapabilityProfile) || (e.Environment != "dev" && e.Environment != "prod") || e.Generation == 0 {
			return errors.New("invalid managed capability identity")
		}
	}
	if len(e.Scopes) > 8 {
		return errors.New("invalid identity")
	}
	for _, scope := range e.Scopes {
		if !scopePattern.MatchString(scope) {
			return errors.New("invalid identity")
		}
	}
	// Provenance is audit metadata, not a routing key: bounded length
	// only, no id pattern. A subject without its issuer (or the reverse)
	// is a half-claim — rejected.
	if len(e.Issuer) > MaxProvenanceBytes || len(e.Subject) > MaxProvenanceBytes || (e.Issuer == "") != (e.Subject == "") {
		return errors.New("invalid identity provenance")
	}
	return nil
}
