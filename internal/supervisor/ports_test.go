package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Port allocation must never hand the same loopback port to two tracked
// runtimes, even when hundreds of contexts hash onto a crowded range.
func TestPortAllocationNeverCollidesAcrossHundredsOfContexts(t *testing.T) {
	m, _ := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("unexpected docker call") }, nil)
	seen := map[int]string{}
	for i := 0; i < 600; i++ {
		key := fmt.Sprintf("principal-%d\x00context-%d\x00gateway", i, i)
		port, err := m.allocatePortLocked(key)
		if err != nil {
			t.Fatalf("allocation %d failed: %v", i, err)
		}
		if port < m.cfg.PortBase || port >= m.cfg.PortBase+m.cfg.PortRange {
			t.Fatalf("allocation %d out of range: %d", i, port)
		}
		if previous, busy := seen[port]; busy {
			t.Fatalf("port %d assigned to both %s and %s", port, previous, key)
		}
		seen[port] = key
		m.items[key] = &runtimeEntry{Runtime: Runtime{Address: fmt.Sprintf("http://127.0.0.1:%d", port)}}
	}
	// Respawning a context prefers its deterministic hash port when free.
	key := "principal-777\x00context-777\x00gateway"
	want := m.cfg.PortBase + int(hashNumber(key)%uint32(m.cfg.PortRange))
	got, err := m.allocatePortLocked(key)
	if err != nil || got != want {
		t.Fatalf("deterministic port changed: %d != %d (err=%v)", got, want, err)
	}
	// Colliding start offsets probe forward instead of overlapping.
	m.items["occupied"] = &runtimeEntry{Runtime: Runtime{Address: fmt.Sprintf("http://127.0.0.1:%d", want)}}
	got, err = m.allocatePortLocked(key)
	if err != nil || got != want+1 {
		t.Fatalf("probe did not skip occupied port: %d != %d (err=%v)", got, want+1, err)
	}
}

// Exhaustion is fail-closed: no runtime starts when the range is full.
func TestPortRangeExhaustionRefusesSpawn(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, errors.New("unexpected docker call") }, nil)
	m.cfg.PortBase, m.cfg.PortRange = 30000, 2
	m.items["other"] = &runtimeEntry{Runtime: Runtime{Address: "http://127.0.0.1:30000"}}
	m.items["other2"] = &runtimeEntry{Runtime: Runtime{Address: "http://127.0.0.1:30001"}}
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || !strings.Contains(err.Error(), "port range exhausted") {
		t.Fatalf("spawn proceeded despite exhausted port range: %v", err)
	}
}

// A restored runtime record whose container is already gone must not block
// respawn after a policy rotation: removing nothing is a no-op, not an error.
func TestEnsureRespawnsAfterStalePolicyWhenContainerMissing(t *testing.T) {
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, errors.New("no such container")
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	b := binding(root)
	key := runtimeKey(b)
	m.mu.Lock()
	m.items[key] = &runtimeEntry{Runtime: Runtime{PrincipalID: "alice", ContextID: "alice", RuntimeID: "alice", RuntimeMode: "gateway", Generation: "gen-9", Container: "ghost", State: Stopped, PolicyVersion: "policy-old", Managed: true}, leases: map[string]Lease{}, restored: true}
	m.mu.Unlock()
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatalf("missing stale container blocked respawn: %v", err)
	}
}
