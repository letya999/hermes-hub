package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Restrictions narrow a published passport ceiling. Null lists inherit;
// empty lists deny all. Guardrails accumulate and can never remove a parent.
type Restrictions struct {
	Grants       []string `json:"grants"`
	Skills       []string `json:"skills"`
	Guardrails   []string `json:"guardrails"`
	MemoryScopes []string `json:"memory_scopes"`
	Mode         string   `json:"mode"`
}

// AllowsUnattendedWrite is the ceiling before the operation's own effect and
// approval checks. A configured guardrail requires an explicit policy decision.
func (r Restrictions) AllowsUnattendedWrite() bool {
	return len(r.Guardrails) == 0 && (r.Mode == "ask-critical" || r.Mode == "full-access")
}

type TemplateRequest struct {
	Subject string `json:"subject"`
	Actor   string `json:"actor"`
	Space   string `json:"space"`
}

type AgentTemplate struct {
	SpaceKind      string       `json:"space_kind"`
	OrganizationID string       `json:"organization_id,omitempty"`
	Subject        Principal    `json:"subject"`
	Actor          Principal    `json:"actor"`
	Space          string       `json:"space"`
	SharedSpace    string       `json:"shared_space,omitempty"`
	Restrictions   Restrictions `json:"restrictions"`
}

func (t AgentTemplate) Revision() (string, error) {
	body, err := json.Marshal(t)
	if err != nil {
		return "", ErrUnavailable
	}
	digest := sha256.Sum256(body)
	return "identity-" + hex.EncodeToString(digest[:]), nil
}
