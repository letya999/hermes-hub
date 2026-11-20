package fake_test

import (
	"net/http"
	"testing"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/contracts/task/v1/conformance"
	"github.com/letya999/hermes-hub/contracts/task/v1/fake"
)

func TestFakePassesConformance(t *testing.T) {
	known := task.RunRecord{JobID: "job-1", RunID: "run-1", Status: "running", UserID: "alice"}
	server := fake.Server{Records: map[string]task.RunRecord{known.JobID: known}}
	conformance.Both(t, server, func(token string) http.Handler { return task.Handler(server, token) }, known)
}
