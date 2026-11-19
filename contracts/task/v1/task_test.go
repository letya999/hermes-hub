package task_test

import (
	"context"
	"net/http"
	"testing"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/contracts/task/v1/conformance"
	"github.com/letya999/hermes-hub/internal/identity"
)

type memServer map[string]task.RunRecord

func (m memServer) Run(_ context.Context, id string) (task.RunRecord, error) {
	if r, ok := m[id]; ok {
		return r, nil
	}
	return task.RunRecord{}, task.ErrNotFound
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	known := task.RunRecord{JobID: "job-1", RunID: "run-1", SessionID: "s", RuntimeGeneration: "g1", CurrentGeneration: "g1", Environment: "dev", Status: "running", ScopeID: "user:u1", Trigger: "message", UserID: "u1", Envelope: identity.Envelope{PrincipalID: "u1", ContextID: "user:u1"}}
	server := memServer{known.JobID: known}
	conformance.Both(t, server, func(token string) http.Handler { return task.Handler(server, token) }, known)
}
