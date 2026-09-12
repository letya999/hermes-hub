package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOwnedInventoryFindsStaleGenerationAndPreservesFailedScan(t *testing.T) {
	var m *Manager
	var generation, key string
	failed := false
	mutations := 0
	var root string
	command := func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" && generation == "" {
			return nil, os.ErrNotExist
		}
		if args[0] == "ps" {
			if failed {
				return nil, errors.New("docker unavailable")
			}
			if !strings.Contains(strings.Join(args, " "), m.ownerID()) {
				t.Error("inventory missing owner filter")
			}
			return []byte("aa\nbb\ncc\n"), nil
		}
		if args[0] == "inspect" && args[2] == "{{json .Config.Labels}}" {
			owner, gen := m.ownerID(), generation
			if args[3] == "bb" {
				gen = "old-generation"
			}
			if args[3] == "cc" {
				owner = "another-supervisor"
			}
			b, _ := json.Marshal(map[string]string{"hermes-hub.owner": owner, "hermes-hub.context": key, "hermes-hub.generation": gen})
			return b, nil
		}
		if args[0] == "inspect" && args[2] == "{{.Name}}" {
			if args[3] == "aa" {
				return []byte(containerName(runtimeKey(binding(root)))), nil
			}
			return []byte("old-container"), nil
		}
		if args[0] == "rm" {
			mutations++
		}
		return []byte("running"), nil
	}
	m, root = testManager(t, command, func(context.Context, string, string) error { return nil })
	b := binding(root)
	runtime, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	generation, key = runtime.Generation, hex.EncodeToString(hashBytes(runtimeKey(b)))
	if err = m.VerifyOwnership(context.Background(), runtime); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generation", "context", "owner"} {
		bad := runtime
		switch field {
		case "generation":
			bad.Generation = "another"
		case "context":
			bad.ContextID = "another"
		case "owner":
			bad.Container = "cc"
		}
		if err = m.VerifyOwnership(context.Background(), bad); err == nil {
			t.Fatalf("%s ownership mismatch accepted", field)
		}
	}
	args := strings.Join(m.runArgsWithGeneration(b, runtime.Container, 19000, generation), " ")
	for _, label := range []string{"hermes-hub.owner=" + m.ownerID(), "hermes-hub.context=" + key, "hermes-hub.generation=" + generation} {
		if !strings.Contains(args, label) {
			t.Fatalf("missing label %s", label)
		}
	}
	if err = m.DetectOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	orphans := m.Orphans()
	if len(orphans) != 1 || orphans[0].Container != "bb" || mutations != 0 {
		t.Fatalf("orphans=%+v mutations=%d", orphans, mutations)
	}
	failed = true
	if err = m.DetectOrphans(context.Background()); err == nil || len(m.Orphans()) != 1 {
		t.Fatal("failed scan lost previous inventory")
	}
	restored, err := New(m.cfg)
	if err != nil || len(restored.Orphans()) != 1 {
		t.Fatalf("inventory not durable: %v", err)
	}
}

func TestReapVerifiesOwnershipAndRechecksNewWork(t *testing.T) {
	for _, mode := range []string{"wrong-generation", "new-job"} {
		t.Run(mode, func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			runtime, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			removed := 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" && args[2] == "{{.Id}}" {
					return []byte(strings.Repeat("a", 64)), nil
				}
				if args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
					return json.Marshal([]map[string]string{{"Type": "bind", "Source": root, "Destination": "/scope"}})
				}
				if args[0] == "rm" {
					removed++
					return nil, nil
				}
				generation := runtime.Generation
				if mode == "wrong-generation" {
					generation = "another"
				} else {
					request := hubruntime.ExecuteRequest{Envelope: identity.Envelope{PrincipalID: b.PrincipalID, ContextID: b.ContextID}, JobID: "new-work", IdempotencyKey: "new-work", Text: "hello"}
					if _, _, err := m.beginJob(request); err != nil {
						t.Error(err)
					}
				}
				return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": generation})
			}
			err = m.Reap(context.Background(), time.Now().Add(time.Hour))
			if mode == "wrong-generation" && err == nil {
				t.Fatal("foreign generation accepted")
			}
			if mode == "new-job" && err != nil {
				t.Fatal(err)
			}
			if removed != 0 || len(m.sem) != 1 {
				t.Fatalf("unsafe removal or released slot: %d %d", removed, len(m.sem))
			}
		})
	}
}

func TestRestoreRejectsReplacementWithoutDiscardingOriginalLeases(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	mutations := 0
	cfg := m.cfg
	cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] != "inspect" {
			mutations++
			return nil, nil
		}
		return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": "replacement"})
	}
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Ensure(context.Background(), b); err == nil {
		t.Fatal("replacement adopted")
	}
	status, _, err := restored.Status(b)
	if err != nil || status.Generation != runtime.Generation || status.Leases != runtime.Leases || mutations != 0 || len(restored.sem) != 1 {
		t.Fatalf("lost original state or mutated replacement: %+v %v mutations=%d", status, err, mutations)
	}
	restored.mu.Lock()
	_, held := restored.items[runtimeKey(b)].leases[lease.ID]
	restored.mu.Unlock()
	if !held {
		t.Fatal("original lease discarded")
	}
}

func TestReapDeletesVerifiedImmutableID(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "verified", false: "invalid-id"}[valid], func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			runtime, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			if err = m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			id := strings.Repeat("a", 64)
			removed := 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "rm" {
					removed++
					if args[2] != id {
						t.Errorf("removed mutable name: %v", args)
					}
					return nil, nil
				}
				if args[2] == "{{.Id}}" {
					if !valid {
						return []byte("invalid"), nil
					}
					return []byte(id), nil
				}
				if args[3] != id {
					t.Errorf("labels checked through mutable name: %v", args)
				}
				if args[2] == "{{json .Mounts}}" {
					return json.Marshal([]map[string]string{{"Type": "bind", "Source": root, "Destination": "/scope"}})
				}
				return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": runtime.Generation})
			}
			err = m.Reap(context.Background(), time.Now().Add(time.Hour))
			if valid && (err != nil || removed != 1) {
				t.Fatalf("verified stop: %v removed=%d", err, removed)
			}
			if !valid && (err == nil || removed != 0) {
				t.Fatalf("invalid identity: %v removed=%d", err, removed)
			}
		})
	}
}

func TestFailedReadinessCleanupVerifiesIDAndPreservesUnknownSlot(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "owned", true: "replacement"}[foreign], func(t *testing.T) {
			removed := 0
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				if args[0] == "rm" {
					removed++
					if len(args[2]) != 64 {
						t.Errorf("cleanup by name: %v", args)
					}
				}
				return []byte("running"), nil
			}, func(context.Context, string, string) error { return nil })
			m.cfg.Probe = func(context.Context, string, string) error {
				if foreign {
					m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
						if args[0] == "rm" {
							removed++
							return nil, nil
						}
						if args[2] == "{{.Id}}" {
							return []byte(strings.Repeat("b", 64)), nil
						}
						return json.Marshal(map[string]string{"hermes-hub.owner": "another"})
					}
				}
				return errors.New("readiness failed")
			}
			if _, err := m.Ensure(context.Background(), binding(root)); err == nil {
				t.Fatal("unready runtime accepted")
			}
			if foreign && (removed != 0 || len(m.sem) != 1) {
				t.Fatalf("replacement removed or unknown slot released: %d %d", removed, len(m.sem))
			}
			if !foreign && (removed != 1 || len(m.sem) != 0) {
				t.Fatalf("owned cleanup failed: %d %d", removed, len(m.sem))
			}
		})
	}
}

func TestRestoredReadinessFailurePreservesRunAndLease(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	command := cfg.Command
	mutations := 0
	cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "inspect" {
			mutations++
		}
		if args[0] == "inspect" && args[2] == "{{.State.Status}}" {
			return []byte("running"), nil
		}
		return command(ctx, args...)
	}
	cfg.Probe = func(context.Context, string, string) error { return errors.New("API temporarily unavailable") }
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Ensure(context.Background(), b); err == nil {
		t.Fatal("unready API accepted")
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	entry := restored.items[runtimeKey(b)]
	if entry.Generation != runtime.Generation || entry.Leases != runtime.Leases || entry.leases[lease.ID].Kind != LeaseStream || mutations != 0 {
		t.Fatalf("live unready runtime discarded: %+v mutations=%d", entry.Runtime, mutations)
	}
}

func TestLostDockerStartAcknowledgementRetainsUnknownCapacity(t *testing.T) {
	for _, removeFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleaned", true: "uncertain"}[removeFails], func(t *testing.T) {
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			started, removals := false, 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				switch args[0] {
				case "run":
					started = true
					return nil, errors.New("lost Docker ACK")
				case "inspect":
					if !started {
						return nil, os.ErrNotExist
					}
					if args[2] == "{{.Id}}" {
						return []byte(strings.Repeat("a", 64)), nil
					}
					if args[2] == "{{json .Mounts}}" {
						return json.Marshal([]map[string]string{{"Type": "bind", "Source": root, "Destination": "/scope"}})
					}
					m.mu.Lock()
					runtime := m.items[runtimeKey(binding(root))].Runtime
					m.mu.Unlock()
					return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(binding(root)))), "hermes-hub.generation": runtime.Generation})
				case "rm":
					removals++
					if removeFails {
						return nil, errors.New("remove unavailable")
					}
					return nil, nil
				}
				return nil, nil
			}
			if _, err := m.Ensure(context.Background(), binding(root)); err == nil {
				t.Fatal("lost ACK accepted")
			}
			want := 0
			if removeFails {
				want = 1
			}
			if removals != 1 || len(m.sem) != want {
				t.Fatalf("removals=%d slots=%d", removals, len(m.sem))
			}
		})
	}
}

func TestRestoredStartingGenerationAdoptsWithoutDuplicate(t *testing.T) {
	m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "inspect" {
			return nil, os.ErrNotExist
		}
		return []byte("running"), nil
	}, func(context.Context, string, string) error { return nil })
	b := binding(root)
	first, err := m.Ensure(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.items[runtimeKey(b)].State = Starting
	err = m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	command := cfg.Command
	starts := 0
	cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "run" {
			starts++
		}
		if args[0] == "inspect" && args[2] == "{{.State.Status}}" {
			return []byte("running"), nil
		}
		return command(ctx, args...)
	}
	restored, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restored.Ensure(context.Background(), b)
	if err != nil || current.Generation != first.Generation || starts != 0 || len(restored.sem) != 1 {
		t.Fatalf("adoption=%+v starts=%d err=%v", current, starts, err)
	}
	before := current.Leases
	restored.statePath = root
	if _, err := restored.Ensure(context.Background(), b); err == nil {
		t.Fatal("non-durable lease accepted")
	}
	current, _, _ = restored.Status(b)
	if current.Leases != before {
		t.Fatal("failed persistence changed lease count")
	}
}

func TestObservedOrphanBlocksNewCapacityWithoutMutation(t *testing.T) {
	mutations := 0
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { mutations++; return nil, nil }, func(context.Context, string, string) error { return nil })
	m.orphans = []OrphanRuntime{{Container: "unknown-owned", Generation: "old"}}
	if _, err := m.Ensure(context.Background(), binding(root)); err == nil || mutations != 0 || len(m.sem) != 0 {
		t.Fatalf("orphan capacity bypassed: err=%v mutations=%d", err, mutations)
	}
}

func TestOrphanReaperRequiresTerminalLedgerAndImmutableOwnership(t *testing.T) {
	for _, scenario := range []string{"terminal", "uncertain", "unknown", "foreign", "current"} {
		t.Run(scenario, func(t *testing.T) {
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				return nil, nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			current, err := m.Ensure(context.Background(), b)
			if err != nil {
				t.Fatal(err)
			}
			generation := "known-old"
			if scenario == "current" {
				generation = current.Generation
			}
			key := hex.EncodeToString(hashBytes(runtimeKey(b)))
			m.orphans = []OrphanRuntime{{Container: "old-name", ContextKey: key, Generation: generation}}
			status := "completed"
			if scenario == "uncertain" {
				status = "uncertain"
			}
			if scenario != "unknown" {
				m.jobs["old"] = jobRecord{Request: hubruntime.ExecuteRequest{Envelope: identity.Envelope{PrincipalID: b.PrincipalID, ContextID: b.ContextID}}, Generation: generation, Status: status}
			}
			id, removals := strings.Repeat("a", 64), 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "rm" {
					removals++
					if args[2] != id || len(args) != 3 {
						t.Error("unsafe orphan deletion")
					}
					return nil, nil
				}
				if args[2] == "{{.Id}}" {
					return []byte(id), nil
				}
				if args[2] == "{{json .Mounts}}" {
					return json.Marshal([]map[string]string{{"Type": "bind", "Source": root, "Destination": "/scope"}})
				}
				owner := m.ownerID()
				if scenario == "foreign" {
					owner = "foreign"
				}
				return json.Marshal(map[string]string{"hermes-hub.owner": owner, "hermes-hub.context": key, "hermes-hub.generation": generation})
			}
			m.ReapOrphans(context.Background())
			want := 0
			if scenario == "terminal" {
				want = 1
			}
			if removals != want {
				t.Fatalf("removals=%d want=%d", removals, want)
			}
		})
	}
}

func TestOwnerLabelsCannotAuthorizeAnotherContextMount(t *testing.T) {
	for _, scenario := range []string{"valid", "other-context", "volume", "missing", "malformed", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			removed := 0
			m, root := testManager(t, func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" {
					return nil, os.ErrNotExist
				}
				if args[0] == "rm" {
					removed++
				}
				return nil, nil
			}, func(context.Context, string, string) error { return nil })
			b := binding(root)
			if _, err := m.Ensure(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			if err := m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			command := m.cfg.Command
			m.cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] != "inspect" || args[2] != "{{json .Mounts}}" {
					return command(ctx, args...)
				}
				kind, source, destination := "bind", root, "/scope"
				switch scenario {
				case "other-context":
					source = root + "-another-user"
				case "volume":
					kind = "volume"
				case "missing":
					destination = "/other"
				case "malformed":
					return []byte("not-json"), nil
				case "oversized":
					return []byte(strings.Repeat("x", 65*1024)), nil
				}
				return json.Marshal([]map[string]string{{"Type": kind, "Source": source, "Destination": destination}})
			}
			err := m.Reap(context.Background(), time.Now().Add(time.Hour))
			if scenario == "valid" {
				if err != nil || removed != 1 {
					t.Fatalf("valid mount rejected: %v", err)
				}
			} else if err == nil || removed != 0 || len(m.sem) != 1 {
				t.Fatalf("cross-context cleanup: error=%v removed=%d slots=%d", err, removed, len(m.sem))
			}
		})
	}
}
