// Package conformance is the task/v1 suite every transport and server must
// pass (SPEC-0100 R2.2).
package conformance

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/shared/testkit"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	task "github.com/letya999/hermes-hub/contracts/task/v1"
)

// Run checks one client: known is a record the server holds.
func Run(t *testing.T, client task.Client, known task.RunRecord) {
	t.Helper()
	ctx := context.Background()
	got, err := client.Run(ctx, known.JobID)
	if err != nil {
		t.Fatalf("known job: %v", err)
	}
	if !reflect.DeepEqual(got, known) {
		t.Fatalf("known job record:\n got %+v\nwant %+v", got, known)
	}
	if _, err := client.Run(ctx, known.JobID+"-missing"); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("missing job: want not_found, got %v", err)
	}
	for _, id := range []string{"", "a/b", "a\nb"} {
		if _, err := client.Run(ctx, id); !errors.Is(err, task.ErrInvalid) {
			t.Fatalf("job id %q: want invalid, got %v", id, err)
		}
	}
}

// Both runs the suite against the in-process transport over server and the
// HTTP transport over handler (the contract Handler or a real server's).
func Both(t *testing.T, server task.Client, handler func(token string) http.Handler, known task.RunRecord) {
	t.Helper()
	const token = "conformance-token"
	testkit.Transports(t, task.NewInProc(server), handler(token), func(url string, hc *http.Client) task.Client { return task.NewHTTPClient(url, token, hc) }, func(t *testing.T, client task.Client) { Run(t, client, known) })
	t.Run("forbidden", func(t *testing.T) {
		srv := httptest.NewServer(handler(token))
		defer srv.Close()
		if _, err := task.NewHTTPClient(srv.URL, "wrong", srv.Client()).Run(context.Background(), known.JobID); !errors.Is(err, task.ErrForbidden) {
			t.Fatalf("wrong token: want forbidden, got %v", err)
		}
	})
}
