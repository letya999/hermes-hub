package toolhub

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/storagebudget"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func setToolHubLimits(t *testing.T, bytes, nextWrite, lifecycleBytes int64, files, lifecycleFiles int) {
	t.Helper()
	t.Setenv("HUB_TOOLHUB_STATE_MAX_BYTES", strconv.FormatInt(bytes, 10))
	t.Setenv("HUB_TOOLHUB_STATE_MAX_NEXT_WRITE_BYTES", strconv.FormatInt(nextWrite, 10))
	t.Setenv("HUB_TOOLHUB_STATE_LIFECYCLE_RESERVE_BYTES", strconv.FormatInt(lifecycleBytes, 10))
	t.Setenv("HUB_TOOLHUB_STATE_MAX_OWNED_FILES", strconv.Itoa(files))
	t.Setenv("HUB_TOOLHUB_STATE_LIFECYCLE_RESERVE_FILES", strconv.Itoa(lifecycleFiles))
}

// The whole store family (snapshot, lock, marker, ledgers, governance,
// audit segments) lives in one owned directory: a save that would push the
// owner's logical bytes over the ceiling must fail closed, not grow the
// state dir without bound.
func TestToolHubSaveFailsClosedWhenStateBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	onboarding := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-quota", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, Revision: 1}
	store := NewStore()
	if err := store.PutOnboarding(onboarding); err != nil {
		t.Fatalf("seed onboarding: %v", err)
	}
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("first save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	size := info.Size()
	// The grown body must stay under NextWrite yet exceed the quota headroom
	// left after the baseline bytes: headroom = size+200 < body(size+size+100)
	// for any realistic snapshot size.
	setToolHubLimits(t, 2*size+201, 2*size+200, 1, 4096, 1)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	onboarding.Error = strings.Repeat("x", int(size)+100)
	if err := loaded.PutOnboarding(onboarding); !errors.Is(err, storagebudget.ErrQuota) {
		t.Fatalf("put over owned budget admitted: %v", err)
	}
}

// The refresh-lease ledger is a ToolHub-owned sidecar in the same directory:
// its atomic rewrite must pass through the owner counter — a write bigger
// than the configured next-write limit fails closed instead of bypassing
// the budget.
func TestToolHubLeaseWriteHonorsNextWriteLimit(t *testing.T) {
	dir := t.TempDir()
	store, _, _ := seededStore(t)
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	setToolHubLimits(t, 1<<20, 100, 1, 4096, 1)
	_, acquired, err := loaded.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
	if acquired || !errors.Is(err, storagebudget.ErrLimits) && !errors.Is(err, storagebudget.ErrQuota) {
		t.Fatalf("lease write bypassed the owner budget: acquired=%v err=%v", acquired, err)
	}
}

// The viewer-link ledger shares the store directory and the same atomic
// rewrite shape as the lease ledger — it must honor the owner budget too.
func TestToolHubViewerLinkWriteHonorsNextWriteLimit(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	if err := store.RegisterDefinition(browserDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := store.PutConnection(browserConnection()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	setToolHubLimits(t, 1<<20, 100, 1, 4096, 1)
	_, _, err = loaded.MintViewerLink(aliceAuth(), "conn-browser", "op-1", time.Now(), 10*time.Minute)
	if !errors.Is(err, storagebudget.ErrLimits) && !errors.Is(err, storagebudget.ErrQuota) {
		t.Fatalf("viewer-link write bypassed the owner budget: %v", err)
	}
}

// The lease persist path runs under leaseMu while saves run under s.mu:
// two owners binding the lazy counter concurrently must not race on the
// Store's storage fields. Run under -race.
func TestToolHubSidecarAndSaveBindCounterRaceFree(t *testing.T) {
	dir := t.TempDir()
	store, _, _ := seededStore(t)
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	done := make(chan error, 2)
	go func() {
		_, _, err := loaded.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
		done <- err
	}()
	go func() {
		done <- loaded.Save(path)
	}()
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent owned write: %v", err)
		}
	}
}

// Owner maintenance reconciles the owned directory against the counter: a
// foreign schema (a symlink the owner never created) must fail closed into a
// recorded, allowlisted error, and the next attempt inside the normal
// interval must back off instead of hammering the filesystem.
func TestToolHubStorageMaintenanceRecordsFailureAndBacksOff(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if store.maintenance.LastSuccess.IsZero() || store.maintenance.LastError != "" {
		t.Fatalf("healthy maintenance not recorded: %+v", store.maintenance)
	}
	if err := os.Symlink(path, filepath.Join(dir, "foreign-link")); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(61 * time.Minute)
	store.maintainStorageLocked(later)
	if store.maintenance.LastError != "cleanup_failed" || !store.maintenance.LastAttempt.Equal(later) {
		t.Fatalf("reconcile failure not recorded safely: %+v", store.maintenance)
	}
	store.maintainStorageLocked(later.Add(time.Minute))
	if !store.maintenance.LastAttempt.Equal(later) {
		t.Fatal("failed maintenance retried without backoff")
	}
}

// Snapshot writes are the ToolHub mutation path: each persisted save is a
// maintenance opportunity, so the first durable write already records an
// attempt without waiting for a separate operator command.
func TestToolHubSaveRunsStorageMaintenance(t *testing.T) {
	dir := t.TempDir()
	store := NewStore()
	path := filepath.Join(dir, "store.json")
	if err := store.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	if store.maintenance.LastAttempt.IsZero() || store.maintenance.LastError != "" {
		t.Fatalf("save did not run bounded maintenance: %+v", store.maintenance)
	}
}

// Terminal compaction may only ever evict records past the fixed retention
// windows — pending and live onboardings and operations are never pruned,
// whatever the storage pressure.
func TestToolHubMaintenanceNeverPrunesLiveState(t *testing.T) {
	store := NewStore()
	now := time.Now()
	live := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-live", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseAwaitingConfirm, Revision: 1, CreatedAt: now.Add(-90 * 24 * time.Hour)}
	removedFresh := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-removed-fresh", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseRemoved, Revision: 1, CreatedAt: now.Add(-24 * time.Hour)}
	removedOld := api.Onboarding{Schema: api.SchemaVersion, OnboardingID: "onboard-removed-old", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", Mode: api.OnboardingCatalog, Phase: api.PhaseRemoved, Revision: 1, CreatedAt: now.Add(-31 * 24 * time.Hour)}
	for _, record := range []api.Onboarding{live, removedFresh, removedOld} {
		if err := store.PutOnboarding(record); err != nil {
			t.Fatalf("seed %s: %v", record.OnboardingID, err)
		}
	}
	opLive := api.Operation{Schema: api.SchemaVersion, OperationID: "op-live", Intent: api.OpIntentInstall, Target: "tool", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", State: api.OpStateRunning, CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now.Add(-30 * 24 * time.Hour)}
	opOld := api.Operation{Schema: api.SchemaVersion, OperationID: "op-old", Intent: api.OpIntentInstall, Target: "tool", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", State: api.OpStateDone, CreatedAt: now.Add(-30 * 24 * time.Hour), UpdatedAt: now.Add(-8 * 24 * time.Hour)}
	opFresh := api.Operation{Schema: api.SchemaVersion, OperationID: "op-fresh", Intent: api.OpIntentInstall, Target: "tool", PrincipalID: "alice", ContextID: "alice", RuntimeID: "runtime", PolicyVersion: "policy-1", State: api.OpStateDone, CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour)}
	for _, op := range []api.Operation{opLive, opOld, opFresh} {
		if err := store.PutOperation(op); err != nil {
			t.Fatalf("seed %s: %v", op.OperationID, err)
		}
	}
	store.compactLocked(now)
	for _, id := range []string{"onboard-live", "onboard-removed-fresh"} {
		if _, ok := store.onboardings[id]; !ok {
			t.Fatalf("live/fresh onboarding %s pruned", id)
		}
	}
	if _, ok := store.onboardings["onboard-removed-old"]; ok {
		t.Fatal("removed onboarding past retention kept")
	}
	for _, id := range []string{"op-live", "op-fresh"} {
		if _, ok := store.operations[id]; !ok {
			t.Fatalf("live/fresh operation %s pruned", id)
		}
	}
	if _, ok := store.operations["op-old"]; ok {
		t.Fatal("terminal operation past retention kept")
	}
}
