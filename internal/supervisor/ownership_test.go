package supervisor

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/letya999/hermes-hub/internal/identity"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerDesktopWindowsScopePathIsExact(t *testing.T) {
	expected := `C:\Users\User\AppData\Local\Temp\owner-a`
	for _, source := range []string{
		`\run\desktop\mnt\host\c\Users\User\AppData\Local\Temp\owner-a`,
		`/run/desktop/mnt/host/C/Users/User/AppData/Local/Temp/owner-a`,
	} {
		if !sameWindowsDockerSource(source, expected) {
			t.Fatalf("Docker Desktop source %q did not map to the exact owner path", source)
		}
	}
	for _, source := range []string{
		`\run\desktop\mnt\host\c\Users\User\AppData\Local\Temp\owner-b`,
		`\run\desktop\mnt\host\..\c\Users\User\AppData\Local\Temp\owner-a`,
		`\run\desktop\mnt\host\1\Users\User\AppData\Local\Temp\owner-a`,
		`\other\desktop\mnt\host\c\Users\User\AppData\Local\Temp\owner-a`,
	} {
		if sameWindowsDockerSource(source, expected) {
			t.Fatalf("non-matching Docker source %q was accepted", source)
		}
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
			// Label-generation verification still guards the unmanaged
			// teardown path; claimed rows verify through spawn instead.
			m.mu.Lock()
			m.items[runtimeKey(b)].Runtime.Managed = false
			m.items[runtimeKey(b)].spawnLease = ""
			m.mu.Unlock()
			removed := 0
			m.cfg.Command = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "inspect" && args[2] == "{{.Id}}" {
					return []byte(strings.Repeat("a", 64)), nil
				}
				if args[0] == "inspect" && args[2] == "{{json .Mounts}}" {
					return json.Marshal([]map[string]string{{"Type": "bind", "Source": filepath.Join(root, "workspace"), "Destination": "/scope"}})
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

// A claimed runtime's "replacement refused" guarantee comes from the row:
// a restore renews the held lease and adopts the SAME generation — no new
// claim, no discarded leases. A row that replaced the workload answers
// not-found instead and the entry drops to a fresh claim.
func TestRestoreRejectsReplacementWithoutDiscardingOriginalLeases(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
	b := binding(root)
	lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	restored, err := newTestManager(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Ensure(context.Background(), b); err != nil {
		t.Fatalf("restored claim rejected: %v", err)
	}
	status, _, err := restored.Status(b)
	if err != nil || status.Generation != runtime.Generation || stub.claimCount() != 1 || len(restored.sem) != 1 {
		t.Fatalf("lost original state or re-claimed: %+v %v claims=%d", status, err, stub.claimCount())
	}
	restored.mu.Lock()
	_, held := restored.items[runtimeKey(b)].leases[lease.ID]
	restored.mu.Unlock()
	if !held {
		t.Fatal("original lease discarded")
	}
}

func TestRestoredReadinessFailurePreservesRunAndLease(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	b := binding(root)
	lease, runtime, err := m.Acquire(context.Background(), b, LeaseStream)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.Probe = func(context.Context, string, string) error { return errors.New("API temporarily unavailable") }
	restored, err := newTestManager(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.Ensure(context.Background(), b); err == nil {
		t.Fatal("unready API accepted")
	}
	restored.mu.Lock()
	defer restored.mu.Unlock()
	entry := restored.items[runtimeKey(b)]
	if entry.Generation != runtime.Generation || len(entry.leases) != 1 || entry.leases[lease.ID].Kind != LeaseStream || entry.spawnLease == "" {
		t.Fatalf("live unready runtime discarded: %+v", entry.Runtime)
	}
}

func TestRestoredStartingGenerationAdoptsWithoutDuplicate(t *testing.T) {
	m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
	stub := m.cfg.Spawn.(*claimStub)
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
	restored, err := newTestManager(t, m.cfg)
	if err != nil {
		t.Fatal(err)
	}
	current, err := restored.Ensure(context.Background(), b)
	if err != nil || current.Generation != first.Generation || stub.claimCount() != 1 || len(restored.sem) != 1 {
		t.Fatalf("adoption=%+v claims=%d err=%v", current, stub.claimCount(), err)
	}
	beforeStatus, _, _ := restored.Status(b)
	before := len(beforeStatus.Consumers)
	restored.statePath = root
	if _, err := restored.Ensure(context.Background(), b); err == nil {
		t.Fatal("non-durable lease accepted")
	}
	current, _, _ = restored.Status(b)
	if len(current.Consumers) != before {
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
					return json.Marshal([]map[string]string{{"Type": "bind", "Source": filepath.Join(root, "workspace"), "Destination": "/scope"}})
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
	for _, scenario := range []string{"valid", "whole-context", "other-context", "volume", "missing", "malformed", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			removed := 0
			m, root := testManager(t, func(context.Context, ...string) ([]byte, error) { return nil, nil }, func(context.Context, string, string) error { return nil })
			b := binding(root)
			if _, err := m.Ensure(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			if err := m.ReleaseBinding(b); err != nil {
				t.Fatal(err)
			}
			// The scope-mount check guards legacy unmanaged records only —
			// managed runtimes never mount /scope. Model the pre-cutover
			// record this verification still protects on teardown.
			m.mu.Lock()
			runtime := m.items[runtimeKey(b)].Runtime
			m.items[runtimeKey(b)].Runtime.Managed = false
			m.items[runtimeKey(b)].spawnLease = ""
			m.mu.Unlock()
			m.cfg.Command = func(ctx context.Context, args ...string) ([]byte, error) {
				if args[0] == "rm" {
					removed++
					return nil, nil
				}
				if args[0] != "inspect" {
					return nil, nil
				}
				switch args[2] {
				case "{{.Id}}":
					return []byte(strings.Repeat("a", 64)), nil
				case "{{json .Config.Labels}}":
					return json.Marshal(map[string]string{"hermes-hub.owner": m.ownerID(), "hermes-hub.context": hex.EncodeToString(hashBytes(runtimeKey(b))), "hermes-hub.generation": runtime.Generation})
				case "{{json .Mounts}}":
				default:
					return nil, nil
				}
				kind, source, destination := "bind", filepath.Join(root, "workspace"), "/scope"
				switch scenario {
				case "whole-context":
					source = root
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
