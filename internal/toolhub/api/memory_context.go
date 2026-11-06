package api

// MemoryContextRequest is the supervisor's pre-dispatch claim. ToolHub checks
// every field against the durable job record before reading the user's vault.
type MemoryContextRequest struct {
	JobID       string `json:"job_id"`
	AdmissionID string `json:"admission_id"`
	Text        string `json:"text"`
}

type MemoryContextResponse struct {
	Status string `json:"status"`
	Core   string `json:"core,omitempty"`
	Recall string `json:"recall,omitempty"`
}
