package spawn_test

// Worker contract HTTP-leg tests: the transport must not impose a fixed
// client timeout or a fixed 1 MiB response cap on exec — a CLI call runs
// as long as its own deadline allows, and an output at the requested cap
// round-trips.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
)

func httpFixture(t *testing.T, server *fake.Server) spawn.Client {
	t.Helper()
	srv := httptest.NewServer(spawn.Handler(server, "tok"))
	t.Cleanup(srv.Close)
	// The ToolHub shape: a client with no fixed timeout — the per-call
	// context deadline is what bounds the request.
	return spawn.NewHTTPClient(srv.URL, "tok", &http.Client{})
}

func claimFixture(t *testing.T, client spawn.Client) spawn.ClaimedWorker {
	t.Helper()
	got, err := client.ClaimWorker(context.Background(), "alice", spawn.ClaimWorkerRequest{
		Org: "local", Owner: "alice", CellKey: "k", Image: "img", Principal: "alice",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return got
}

// TestExecWorkerNoFixedClientTimeout: a server that answers exec past
// the old 5s client default still succeeds inside the caller's deadline.
func TestExecWorkerNoFixedClientTimeout(t *testing.T) {
	server := &fake.Server{ExecFunc: func(in spawn.WorkerExecRequest) (spawn.WorkerExecResult, error) {
		time.Sleep(6 * time.Second)
		return spawn.WorkerExecResult{Stdout: []byte("slow-ok")}, nil
	}}
	client := httpFixture(t, server)
	held := claimFixture(t, client)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := client.ExecWorker(ctx, held.Worker.ID, spawn.WorkerExecRequest{
		Org: "local", Owner: "alice", Lease: held.Lease, Argv: []string{"true"},
	})
	if err != nil {
		t.Fatalf("slow exec failed: %v", err)
	}
	if string(res.Stdout) != "slow-ok" {
		t.Fatalf("exec result=%+v", res)
	}
}

// TestExecWorkerOutputCapRoundTrips: the response cap follows the
// requested MaxOutput — two streams at a 1 MiB cap travel a body far
// past the old 1 MiB envelope.
func TestExecWorkerOutputCapRoundTrips(t *testing.T) {
	const want = 1 << 20
	server := &fake.Server{ExecFunc: func(in spawn.WorkerExecRequest) (spawn.WorkerExecResult, error) {
		return spawn.WorkerExecResult{
			Stdout: make([]byte, in.MaxOutput), Stderr: make([]byte, in.MaxOutput),
		}, nil
	}}
	client := httpFixture(t, server)
	held := claimFixture(t, client)
	res, err := client.ExecWorker(context.Background(), held.Worker.ID, spawn.WorkerExecRequest{
		Org: "local", Owner: "alice", Lease: held.Lease, Argv: []string{"yes"}, MaxOutput: want,
	})
	if err != nil {
		t.Fatalf("capped exec failed to decode: %v", err)
	}
	if len(res.Stdout) != want || len(res.Stderr) != want {
		t.Fatalf("exec streams=%d+%d, want %d each", len(res.Stdout), len(res.Stderr), want)
	}
}

// TestExecWorkerDockerEnvRefused: env names that would reconfigure the
// controller's docker CLI are refused at the contract — on both legs.
func TestExecWorkerDockerEnvRefused(t *testing.T) {
	client := httpFixture(t, &fake.Server{})
	held := claimFixture(t, client)
	req := spawn.WorkerExecRequest{
		Org: "local", Owner: "alice", Lease: held.Lease, Argv: []string{"true"},
		Env: map[string]string{"DOCKER_HOST": "tcp://evil.invalid:2375"},
	}
	if _, err := client.ExecWorker(context.Background(), held.Worker.ID, req); err == nil {
		t.Fatal("DOCKER_* env accepted at the contract")
	}
}
