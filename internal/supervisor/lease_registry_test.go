package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
)

// A consumer lease is fenced to its runtime generation: renew and release by
// an old lease id can never touch the replacement generation, so a cancelled
// generation's renewals cannot resurrect demand on a newer runtime. The same
// lease must surface through runtime status as an allowlisted consumer —
// identity, scope, kind and expiry only, never request payloads.
func TestLeaseGenerationFence(t *testing.T) {
	m, _ := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	request := hubruntime.ExecuteRequest{Envelope: identity.TelegramEnvelope("alice", 1, "alice", "policy-1"), JobID: "lease-job", IdempotencyKey: "lease-job", UserID: "alice", ActorID: "alice", ScopeID: "user:alice", OrganizationID: "personal"}
	b, err := m.bindingFor(request)
	if err != nil {
		t.Fatal(err)
	}
	if b.ScopeID != "user:alice" {
		t.Fatalf("binding scope=%q", b.ScopeID)
	}
	lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Scope != "user:alice" {
		t.Fatalf("lease scope=%q", lease.Scope)
	}
	status, ok, err := m.Status(b)
	if err != nil || !ok {
		t.Fatalf("status ok=%v err=%v", ok, err)
	}
	if len(status.Consumers) != 1 || status.Consumers[0].ID != lease.ID || status.Consumers[0].Kind != LeaseStream || status.Consumers[0].Scope != "user:alice" {
		t.Fatalf("status consumers=%+v", status.Consumers)
	}
	if status.WaitReason != "stream" {
		t.Fatalf("wait reason=%q, want stream", status.WaitReason)
	}
	recorder := httptest.NewRecorder()
	m.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/runtimes", nil).WithContext(context.Background()))
	if recorder.Code == http.StatusUnauthorized {
		request := httptest.NewRequest(http.MethodGet, "/v1/runtimes", nil)
		request.Header.Set("Authorization", "Bearer secret")
		recorder = httptest.NewRecorder()
		m.Handler().ServeHTTP(recorder, request)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body)
	}
	var listed []Runtime
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed) != 1 || len(listed[0].Consumers) != 1 || listed[0].Consumers[0].ID != lease.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	before := lease.ExpiresAt
	m.mu.Lock()
	m.cfg.Now = func() time.Time { return time.Unix(100, 0).Add(time.Minute) }
	m.mu.Unlock()
	if err := m.RenewLease(lease.ID); err != nil {
		t.Fatalf("same-generation renew: %v", err)
	}
	m.mu.Lock()
	renewed := m.items[runtimeKey(b)].leases[lease.ID]
	m.mu.Unlock()
	if renewed.RenewedAt.IsZero() || !renewed.ExpiresAt.After(before) {
		t.Fatalf("renew did not extend the lease: %+v", renewed)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].Generation = runtime.Generation + "-replaced"
	m.mu.Unlock()
	if err := m.RenewLease(lease.ID); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale lease renew=%v", err)
	}
	if err := m.ReleaseLease(lease.ID); err == nil {
		t.Fatal("stale lease released a replacement generation")
	}
	// A superseded consumer's lease is swept by demand, not by its own stale
	// handle: the fenced generation keeps its replacement unharmed until the
	// stale-hold sweep clears it.
	m.sweepStaleLeasesLocked(m.cfg.Now())
	m.mu.Lock()
	entry := m.items[runtimeKey(b)]
	if _, ok := entry.leases[lease.ID]; ok {
		t.Fatal("stale consumer lease survived the sweep")
	}
	m.mu.Unlock()
	if err := m.RenewLease(" "); err == nil {
		t.Fatal("blank lease id accepted")
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].leases["ghost"] = Lease{ID: "ghost", Kind: LeaseUncertain, Generation: entry.Generation, Owner: "supervisor", ExpiresAt: m.cfg.Now().Add(time.Minute)}
	entry.Leases++
	m.mu.Unlock()
	status, ok, _ = m.Status(b)
	if !ok || len(status.Consumers) != 1 || status.Consumers[0].ID != "ghost" {
		t.Fatalf("status after ghost=%+v ok=%v", status.Consumers, ok)
	}
	if status.WaitReason != "observation" {
		t.Fatalf("wait reason=%q, want observation", status.WaitReason)
	}
	m.mu.Lock()
	for _, entry := range m.items {
		entry.Generation += "-moved"
		if err := m.persistLocked(); err != nil {
			m.mu.Unlock()
			t.Fatal(err)
		}
	}
	m.mu.Unlock()
	if err := m.RenewLease("ghost"); !errors.Is(err, errStaleLease) && (err == nil || !strings.Contains(err.Error(), "stale")) {
		t.Fatalf("stale renew=%v", err)
	}
}
