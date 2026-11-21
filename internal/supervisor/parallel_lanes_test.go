package supervisor

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

func TestManagedJobsShareOneRuntimeWithFiveRunCap(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	b, err := m.normalize(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	jobs := make([]hubruntime.ExecuteRequest, 6)
	for i := range jobs {
		id := "parallel-" + strconv.Itoa(i)
		e := identity.TelegramEnvelope("alice", 11, "alice", "policy-1")
		e.ConversationID = id
		jobs[i] = hubruntime.ExecuteRequest{Envelope: e, JobID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", Text: id}
		if _, _, err := m.beginJob(jobs[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		mode, err := m.claimManagedLane(t.Context(), jobs[i], b)
		if err != nil {
			t.Fatal(err)
		}
		if mode != "gateway" {
			t.Fatalf("run %d uses runtime mode %s", i, mode)
		}
		bound, err := m.bindingFor(context.Background(), jobs[i])
		if err != nil || bound.RuntimeMode != mode {
			t.Fatalf("job route %d = %s, err=%v", i, bound.RuntimeMode, err)
		}
	}
	wait, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := m.claimManagedLane(wait, jobs[5], b); err == nil {
		t.Fatal("sixth job bypassed the five-lane ceiling")
	}
	if err := m.finishJobGeneration(jobs[1], hubruntime.ExecuteResponse{JobID: jobs[1].JobID, Status: "completed"}, "completed", ""); err != nil {
		t.Fatal(err)
	}
	mode, err := m.claimManagedLane(t.Context(), jobs[5], b)
	if err != nil || mode != "gateway" {
		t.Fatalf("released lane = %s, err=%v", mode, err)
	}
}

func TestManagedSameConversationWaitsInAdmissionOrder(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return []byte("running"), nil }, func(context.Context, string, string) error { return nil })
	b, err := m.normalize(context.Background(), binding(root))
	if err != nil {
		t.Fatal(err)
	}
	request := func(id string) hubruntime.ExecuteRequest {
		return hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 11, "alice", "policy-1"), JobID: id, IdempotencyKey: id, OrganizationID: "personal", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", Channel: "telegram_bot", Trigger: "message", Text: id}
	}
	first, second := request("fifo-first"), request("fifo-second")
	for _, job := range []hubruntime.ExecuteRequest{first, second} {
		if _, _, err := m.beginJob(job); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		id, mode string
		err      error
	}
	results := make(chan result, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for _, job := range []hubruntime.ExecuteRequest{first, second} {
		go func() {
			mode, err := m.claimManagedLane(ctx, job, b)
			results <- result{job.JobID, mode, err}
		}()
	}
	select {
	case got := <-results:
		if got.id != first.JobID || got.mode != "gateway" || got.err != nil {
			t.Fatalf("first claim=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("same-conversation claims deadlocked")
	}
	select {
	case got := <-results:
		t.Fatalf("second claim bypassed FIFO: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	if err := m.finishJobGeneration(first, hubruntime.ExecuteResponse{JobID: first.JobID, Status: "completed"}, "completed", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got.id != second.JobID || got.mode != "gateway" || got.err != nil {
			t.Fatalf("second claim=%+v", got)
		}
	case <-ctx.Done():
		t.Fatal("second claim did not resume")
	}
}

func TestManagedRunArgsMaterializeConcurrently(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 2 && args[0] == "inspect" && args[2] == "{{.State.Status}}" {
			return nil, errors.New("missing")
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	var group sync.WaitGroup
	errors := make(chan error, 5)
	for _, mode := range [...]string{"gateway", "gateway-2", "gateway-3", "gateway-4", "gateway-5"} {
		group.Add(1)
		go func() {
			defer group.Done()
			b := binding(root)
			b.RuntimeMode = mode
			_, err := m.Ensure(t.Context(), b)
			errors <- err
		}()
	}
	group.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}
