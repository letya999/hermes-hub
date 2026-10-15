package api

import ()

// CliExecRequest is the /cli-exec contract. Plan carries the immutable
// definition identity and execution policy the same way /admit does.
type CliExecRequest struct {
	Plan            ControllerPlan    `json:"plan"`
	Command         string            `json:"command"`
	Args            []string          `json:"args,omitempty"`
	Env             []string          `json:"env,omitempty"`
	Principal       string            `json:"principal_id"`
	ContextID       string            `json:"context_id"`
	BindingID       string            `json:"binding_id"`
	JobID           string            `json:"job_id,omitempty"`
	Lifecycle       string            `json:"lifecycle,omitempty"`
	ToolboxID       string            `json:"toolbox_id,omitempty"`
	Brokered        []CliBrokeredCred `json:"brokered,omitempty"`
	Stateless       bool              `json:"stateless,omitempty"`
	InstallPackages bool              `json:"install_packages,omitempty"`
	Workspace       CliWorkspace      `json:"workspace,omitempty"`
}

type CliExecResponse struct {
	Receipt  AdmissionReceipt `json:"receipt"`
	Output   string           `json:"output"`
	ExitCode int              `json:"exit_code"`
	// Error carries a completed-but-flagged outcome the caller maps to a
	// typed error — "output-limit" today. Denials stay on HTTP errors.
	Error string `json:"error,omitempty"`
}

// CliReleaseRequest kills warm cells selected by principal/binding/job.
type CliReleaseRequest struct {
	PrincipalID string `json:"principal_id"`
	BindingID   string `json:"binding_id,omitempty"`
	JobID       string `json:"job_id,omitempty"`
	Reason      string `json:"reason,omitempty"`
}
