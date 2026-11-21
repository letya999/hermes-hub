package supervisor

// The claim stub answers managed-runtime claims the way spawn-controller's
// contract does: a fixed endpoint (the harness stubs Probe anyway), a fresh
// lease, and the request recorded for assertions.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
)

type claimStub struct {
	mu         sync.Mutex
	address    string
	err        error
	spec       spawnv1.EffectiveSpec
	specErr    error
	renewErr   error
	drainErr   error
	generation int64
	claimHook  func() // runs inside ClaimRuntime; may block to model a slow spawn
	claims     []spawnv1.ClaimRuntimeRequest
	renewed    []string
	released   []string
	drained    []spawnv1.DrainRuntimesRequest
}

func (s *claimStub) ClaimRuntime(_ context.Context, _ string, in spawnv1.ClaimRuntimeRequest) (spawnv1.ClaimedRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimHook != nil {
		s.claimHook()
	}
	s.claims = append(s.claims, in)
	if s.err != nil {
		return spawnv1.ClaimedRuntime{}, s.err
	}
	s.generation++
	address := s.address
	if address == "" {
		address = "http://127.0.0.1:9"
	}
	return spawnv1.ClaimedRuntime{
		Runtime: spawnv1.Runtime{ID: fmt.Sprintf("rt-stub-%d", s.generation), Address: address, Generation: s.generation},
		Lease:   fmt.Sprintf("ls-stub-%d", s.generation),
	}, nil
}

func (s *claimStub) RenewLease(_ context.Context, id string, _ spawnv1.LeaseRequest) (spawnv1.Runtime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewed = append(s.renewed, id)
	if s.renewErr != nil {
		return spawnv1.Runtime{}, s.renewErr
	}
	return spawnv1.Runtime{ID: "rt-stub-1"}, nil
}

func (s *claimStub) ReleaseLease(_ context.Context, id string, _ spawnv1.LeaseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, id)
	return nil
}

func (s *claimStub) Effective(context.Context, spawnv1.Tenant, string) (spawnv1.EffectiveSpec, error) {
	if s.specErr != nil {
		return spawnv1.EffectiveSpec{}, s.specErr
	}
	return s.spec, nil
}

func (s *claimStub) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: s.spec.Documents}, s.specErr
}

func (s *claimStub) DrainRuntimes(_ context.Context, _ string, in spawnv1.DrainRuntimesRequest) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drained = append(s.drained, in)
	return 1, s.drainErr
}

func (s *claimStub) claimed() []spawnv1.ClaimRuntimeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spawnv1.ClaimRuntimeRequest(nil), s.claims...)
}

func (s *claimStub) claimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.claims)
}

func (s *claimStub) releaseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.released)
}

func (s *claimStub) drainCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.drained)
}

func (s *claimStub) setRenewErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewErr = err
}

// The claimed runtime's endpoint becomes the supervisor's forwarding
// address, the generation maps to the entry's generation, and the claim
// lease stays held on the entry — renewed on reuse, released on reap (S7c).
func TestEnsureClaimsManagedRuntimeThroughSpawn(t *testing.T) {
	stub := &claimStub{address: "http://claimed:8080"}
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Spawn = stub
	writeManagedFixtures(t, ctxRoot)
	rt, err := m.Ensure(t.Context(), binding(ctxRoot))
	if err != nil {
		t.Fatal(err)
	}
	if rt.Address != "http://claimed:8080" || rt.ComputeID != "rt-stub-1" || rt.Generation != "gen-1" {
		t.Fatalf("claimed runtime mapped wrong: %+v", rt)
	}
	claims := stub.claimed()
	if len(claims) != 1 {
		t.Fatalf("claims=%d, want 1", len(claims))
	}
	in := claims[0]
	if in.Org != "local" || in.Owner != "alice" || in.Agent == "" || in.RuntimeID != "alice" ||
		in.RuntimeMode != "gateway" || in.UserID != "alice" || in.OrganizationID != "personal" ||
		in.PolicyVersion != "policy-1" || in.IdentityRevision == "" || in.Restrictions == nil {
		t.Fatalf("claim request incomplete: %+v", in)
	}
	if len(stub.released) != 0 {
		t.Fatalf("serving lease released early: %v", stub.released)
	}
}

// A claim failure surfaces as the Ensure error and frees the capacity slot.
func TestEnsureSurfacesClaimFailure(t *testing.T) {
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Spawn = &claimStub{err: spawnv1.ErrUnavailable}
	writeManagedFixtures(t, ctxRoot)
	if _, err := m.Ensure(t.Context(), binding(ctxRoot)); err == nil {
		t.Fatal("claim failure accepted")
	}
}

// Reusing a live entry re-arms the spawn hold instead of claiming again —
// the busy lease is what keeps the warm-pool sweeper from draining a
// serving workload (warm idle TTL would otherwise reap it).
func TestEnsureReuseRenewsSpawnLease(t *testing.T) {
	stub := &claimStub{address: "http://claimed:8080"}
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Spawn = stub
	writeManagedFixtures(t, ctxRoot)
	if _, err := m.Ensure(t.Context(), binding(ctxRoot)); err != nil {
		t.Fatal(err)
	}
	rt, err := m.Ensure(t.Context(), binding(ctxRoot))
	if err != nil {
		t.Fatal(err)
	}
	if rt.ComputeID != "rt-stub-1" {
		t.Fatalf("reuse claimed a new runtime: %+v", rt)
	}
	if len(stub.claims) != 1 || len(stub.renewed) != 1 || stub.renewed[0] != "ls-stub-1" {
		t.Fatalf("claims=%v renewed=%v, want one claim + one renew", stub.claims, stub.renewed)
	}
}

// A renew that reports the lease gone means the workload drained under the
// entry — the supervisor drops it and claims a fresh generation.
func TestEnsureReclaimsAfterSpawnDrain(t *testing.T) {
	stub := &claimStub{address: "http://claimed:8080"}
	m, ctxRoot := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	m.cfg.Spawn = stub
	writeManagedFixtures(t, ctxRoot)
	if _, err := m.Ensure(t.Context(), binding(ctxRoot)); err != nil {
		t.Fatal(err)
	}
	stub.renewErr = spawnv1.ErrNotFound
	defer func() { stub.renewErr = nil }()
	rt, err := m.Ensure(t.Context(), binding(ctxRoot))
	if err != nil {
		t.Fatal(err)
	}
	if rt.ComputeID != "rt-stub-2" || rt.Generation != "gen-2" {
		t.Fatalf("re-claim failed: %+v", rt)
	}
	if len(stub.claims) != 2 {
		t.Fatalf("claims=%d, want 2 (re-claim after drain)", len(stub.claims))
	}
}
