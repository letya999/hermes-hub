// Package identity defines trusted routing identifiers shared by the gateway and runtime.
package identity

import (
	"errors"
	"regexp"
	"strconv"
)

const Schema = 1

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

type Envelope struct {
	Schema             int    `json:"identity_schema"`
	PrincipalID        string `json:"principal_id"`
	ExternalIdentityID string `json:"external_identity_id"`
	ContextID          string `json:"context_id"`
	RuntimeID          string `json:"runtime_id"`
	ConversationID     string `json:"conversation_id"`
	DeliveryTargetID   string `json:"delivery_target_id"`
	PolicyVersion      string `json:"policy_version"`
}

func ValidID(id string) bool { return idPattern.MatchString(id) }

func TelegramEnvelope(principal string, sender int64, runtime, policy string) Envelope {
	transportID := "telegram-" + strconv.FormatInt(sender, 10)
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
	if e.PrincipalID != principal || e.ContextID != context || e.RuntimeID != runtime || e.PolicyVersion != policy {
		return errors.New("identity binding mismatch")
	}
	return nil
}
