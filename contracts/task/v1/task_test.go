package task_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
	"github.com/letya999/hermes-hub/contracts/task/v1/conformance"
	"github.com/letya999/hermes-hub/contracts/task/v1/fake"
	"github.com/letya999/hermes-hub/internal/identity"
)

type failingServer struct{ err error }

func (s failingServer) Run(context.Context, string) (task.RunRecord, error) {
	if s.err != nil {
		return task.RunRecord{}, s.err
	}
	return task.RunRecord{}, fmt.Errorf("private-diagnostic-marker")
}

func TestWrappedFailureIsOpaqueAcrossTransports(t *testing.T) {
	for _, kind := range []error{task.ErrNotFound, task.ErrConflict, task.ErrForbidden, task.ErrInvalid, task.ErrUnavailable} {
		server := failingServer{err: fmt.Errorf("private-diagnostic-marker: %w", kind)}
		srv := httptest.NewServer(task.Handler(server, "token"))
		for _, client := range []task.Client{task.NewInProc(server), task.NewHTTPClient(srv.URL, "token", srv.Client())} {
			_, err := client.Run(context.Background(), "job-1")
			if !errors.Is(err, kind) || strings.Contains(err.Error(), "private-diagnostic-marker") {
				t.Errorf("wrapped %v: got %v", kind, err)
			}
		}
		srv.Close()
	}
}

func TestUnexpectedFailureIsOpaqueAcrossTransports(t *testing.T) {
	server := failingServer{}
	httpServer := httptest.NewServer(task.Handler(server, "test-token"))
	defer httpServer.Close()
	for _, client := range []task.Client{task.NewInProc(server), task.NewHTTPClient(httpServer.URL, "test-token", httpServer.Client())} {
		_, err := client.Run(context.Background(), "job-1")
		if !errors.Is(err, task.ErrUnavailable) {
			t.Fatal("unexpected failure lost its unavailable error class")
		}
		if strings.Contains(err.Error(), "private-diagnostic-marker") {
			t.Error("unexpected failure exposed backend diagnostics")
		}
	}
}

func TestContractConformanceInProcAndHTTP(t *testing.T) {
	known := task.RunRecord{JobID: "job-1", RunID: "run-1", SessionID: "s", RuntimeGeneration: "g1", CurrentGeneration: "g1", Environment: "dev", Status: "running", ScopeID: "user:u1", Trigger: "message", UserID: "u1", Envelope: identity.Envelope{PrincipalID: "u1", ContextID: "user:u1"}}
	server := fake.Server{Records: map[string]task.RunRecord{known.JobID: known}}
	conformance.Both(t, server, func(token string) http.Handler { return task.Handler(server, token) }, known)
}

func TestEncodedJobIDIsNotDecodedTwice(t *testing.T) {
	known := task.RunRecord{JobID: "job%2F1", RunID: "run-1"}
	server := fake.Server{Records: map[string]task.RunRecord{known.JobID: known}}
	conformance.Both(t, server, func(token string) http.Handler { return task.Handler(server, token) }, known)
}
