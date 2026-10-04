package supervisor

import (
	"context"
	"encoding/hex"
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

// A dead same-name container from an older generation is reclaimed so a new
// generation can spawn; a foreign squatter is never touched.
func TestEnsureReclaimsDeadStaleContainer(t *testing.T) {
	var order []string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return []byte("exited"), nil
		case args[0] == "rm":
			order = append(order, "rm "+args[len(args)-1])
			return []byte(""), nil
		case args[0] == "run":
			order = append(order, "run "+args[3])
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	b := binding(root)
	key := runtimeKey(b)
	name := containerName(key)
	// Seed the wrapper's label inventory as if gen-0 created this container.
	if _, err := m.command(context.Background(), "run", "-d", "--name", name,
		"--label", "hermes-hub.owner="+m.ownerID(),
		"--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(key)),
		"--label", "hermes-hub.generation=gen-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatalf("stale container blocked respawn: %v", err)
	}
	if len(order) != 3 || order[0] != "run "+name || order[1] != "rm "+name || order[2] != "run "+name {
		t.Fatalf("stale container not reclaimed before spawn: %v", order)
	}
}

func TestEnsureReclaimsRunningStaleGeneration(t *testing.T) {
	var order []string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return []byte("running"), nil
		case args[0] == "rm":
			order = append(order, "rm "+args[len(args)-1])
			return []byte(""), nil
		case args[0] == "run":
			order = append(order, "run "+args[3])
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	b := binding(root)
	key := runtimeKey(b)
	name := containerName(key)
	if _, err := m.command(context.Background(), "run", "-d", "--name", name,
		"--label", "hermes-hub.owner="+m.ownerID(),
		"--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(key)),
		"--label", "hermes-hub.generation=gen-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), b); err != nil {
		t.Fatalf("stale generation blocked respawn: %v", err)
	}
	if len(order) != 3 || order[0] != "run "+name || order[1] != "rm "+name || order[2] != "run "+name {
		t.Fatalf("stale generation not reclaimed before spawn: %v", order)
	}
}

// A running same-generation container is reused through the port it actually
// publishes, not the freshly allocated one.
func TestEnsureReusesPublishedPortOnRecovery(t *testing.T) {
	var probed string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return []byte("running"), nil
		case args[0] == "port":
			return []byte("8080/tcp -> 127.0.0.1:29999\n"), nil
		}
		return []byte(""), nil
	}
	probe := func(_ context.Context, address, _ string) error { probed = address; return nil }
	m, root := testManager(t, command, probe)
	b := binding(root)
	key := runtimeKey(b)
	name := containerName(key)
	if _, err := m.command(context.Background(), "run", "-d", "--name", name,
		"--label", "hermes-hub.owner="+m.ownerID(),
		"--label", "hermes-hub.context="+hex.EncodeToString(hashBytes(key)),
		"--label", "hermes-hub.generation=gen-1",
		"--mount", "type=bind,src="+root+",dst=/scope"); err != nil {
		t.Fatal(err)
	}
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatalf("same-generation running container not reused: %v", err)
	}
	if probed != "http://127.0.0.1:29999" || runtime.Address != "http://127.0.0.1:29999" {
		t.Fatalf("reuse did not discover published port: probed=%s address=%s", probed, runtime.Address)
	}
}

// A foreign container occupying the deterministic name refuses the spawn;
// label-verified reclaim never removes another owner's workload.
func TestEnsureRefusesForeignSameNameContainer(t *testing.T) {
	var removed []string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		switch {
		case args[0] == "inspect" && args[2] == "{{.State.Status}}":
			return []byte("running"), nil
		case args[0] == "rm":
			removed = append(removed, args[len(args)-1])
		}
		return []byte(""), nil
	}
	m, root := testManager(t, command, func(context.Context, string, string) error { return nil })
	b := binding(root)
	name := containerName(runtimeKey(b))
	if _, err := m.command(context.Background(), "run", "-d", "--name", name,
		"--label", "hermes-hub.owner=foreign-owner",
		"--label", "hermes-hub.context="+hex.EncodeToString(hashBytes("other")),
		"--label", "hermes-hub.generation=gen-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), b); err == nil {
		t.Fatal("foreign same-name container was reclaimed or ignored")
	}
	if len(removed) != 0 {
		t.Fatalf("foreign container removed: %v", removed)
	}
}
