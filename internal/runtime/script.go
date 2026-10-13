package runtime

import "time"

// ScriptRequest uses the ordinary owner envelope without starting Hermes.
type ScriptRequest struct {
	ExecuteRequest
	DueAt time.Time `json:"due_at"`
}
