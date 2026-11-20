// Package fake is a foreign task-engine fixture, not a service implementation.
// It passes the same contract conformance suite as the real owner.
package fake

import (
	"context"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/shared/httpapi"
)

// Server contains immutable records supplied by the test. It is safe for
// concurrent reads; tests construct a new Server rather than mutating Records.
type Server struct{ Records map[string]task.RunRecord }

func (s Server) Run(ctx context.Context, id string) (task.RunRecord, error) {
	if err := ctx.Err(); err != nil {
		return task.RunRecord{}, err
	}
	if !task.ValidJobID(id) {
		return task.RunRecord{}, task.ErrInvalid
	}
	if record, ok := s.Records[id]; ok {
		return httpapi.Copy(record)
	}
	return task.RunRecord{}, task.ErrNotFound
}
