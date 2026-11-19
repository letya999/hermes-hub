package identity

// Restrictions narrow a published passport ceiling. Null lists inherit;
// empty lists deny all. Guardrails accumulate and can never remove a parent.
type Restrictions struct {
	Grants       []string `json:"grants"`
	Skills       []string `json:"skills"`
	Guardrails   []string `json:"guardrails"`
	MemoryScopes []string `json:"memory_scopes"`
	Mode         string   `json:"mode"`
}

type TemplateRequest struct {
	Subject string `json:"subject"`
	Actor   string `json:"actor"`
	Space   string `json:"space"`
}

type AgentTemplate struct {
	Subject      Principal    `json:"subject"`
	Actor        Principal    `json:"actor"`
	Space        string       `json:"space"`
	SharedSpace  string       `json:"shared_space,omitempty"`
	Restrictions Restrictions `json:"restrictions"`
}
