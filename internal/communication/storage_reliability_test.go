package communication

import (
	"bytes"
	"context"
	"errors"
	hubruntime "github.com/letya999/hermes-hub/internal/runtime"
	"github.com/letya999/hermes-hub/internal/storagebudget"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func storageQuotaFixture(t *testing.T, limit, next, reserve string) string {
	t.Helper()
	t.Setenv("HUB_SPOOL_MAX_BYTES", limit)
	t.Setenv("HUB_SPOOL_MAX_NEXT_WRITE_BYTES", next)
	t.Setenv("HUB_SPOOL_LIFECYCLE_RESERVE_BYTES", reserve)
	root := filepath.Join(t.TempDir(), "owned-spool")
	if _, err := NewSpool(root); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRemoveWithoutOwnerCounterFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "retained.bin")
	if err := os.WriteFile(path, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	var s Spool
	if err := s.removeOwned(path); !errors.Is(err, storagebudget.ErrUnavailable) {
		t.Fatalf("unbound owner removal should fail closed: %v", err)
	}
	if body, err := os.ReadFile(path); err != nil || string(body) != "retained" {
		t.Fatal("unbound owner removal changed retained data", err)
	}
}

func TestCleanupFailureVisibleAndBackedOff(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "mappings", "by-key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "mappings")); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s}
	now := time.Now().UTC()
	g.maintainStorage(now)
	if g.maintenance.LastError != "cleanup_failed" || !g.maintenance.LastAttempt.Equal(now) {
		t.Fatalf("cleanup error not recorded safely: %+v", g.maintenance)
	}
	g.maintainStorage(now.Add(time.Minute))
	if !g.maintenance.LastAttempt.Equal(now) {
		t.Fatal("cleanup failure retried before backoff")
	}
}

func TestMaintenanceSuccessPreservesDefaultRetention(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	path := filepath.Join(root, "done", "recent.json")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s}
	g.maintenance.LastError = "cleanup_failed"
	g.maintainStorage(now)
	if !g.maintenance.LastSuccess.Equal(now) || g.maintenance.LastError != "" {
		t.Fatalf("successful cleanup status not recorded: %+v", g.maintenance)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("default retention pruned recent terminal record", err)
	}
}

func TestMaintenanceNormalIntervalIsHourly(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s}
	now := time.Now().UTC()
	g.maintainStorage(now)
	g.maintainStorage(now.Add(6 * time.Minute))
	if !g.maintenance.LastAttempt.Equal(now) {
		t.Fatal("normal maintenance repeated before one hour")
	}
}

func TestMaintenanceByteHighWaterRunsSooner(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s}
	now := time.Now().UTC()
	g.maintainStorage(now)
	if err := os.MkdirAll(filepath.Join(root, "archive"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "archive", "fixture.bin"), make([]byte, 53*1024), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.storage.Reconcile(); err != nil {
		t.Fatal(err)
	}
	g.maintainStorage(now.Add(6 * time.Minute))
	if !g.maintenance.LastAttempt.Equal(now.Add(6 * time.Minute)) {
		t.Fatal("byte high-water did not trigger bounded maintenance")
	}
}

func TestMaintenanceFileHighWaterRunsSooner(t *testing.T) {
	t.Setenv("HUB_SPOOL_MAX_OWNED_FILES", "100")
	t.Setenv("HUB_SPOOL_LIFECYCLE_RESERVE_FILES", "10")
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s}
	now := time.Now().UTC()
	g.maintainStorage(now)
	if err := os.MkdirAll(filepath.Join(root, "archive"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.storage.Reconcile(); err != nil {
		t.Fatal(err)
	}
	need := 80 - s.storage.Snapshot().Files
	for i := 0; i < need; i++ {
		if err := os.WriteFile(filepath.Join(root, "archive", "fixture-"+strconv.Itoa(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.storage.Reconcile(); err != nil {
		t.Fatal(err)
	}
	g.maintainStorage(now.Add(6 * time.Minute))
	if !g.maintenance.LastAttempt.Equal(now.Add(6 * time.Minute)) {
		t.Fatal("file high-water did not trigger bounded maintenance")
	}
}

func TestControlLoopRecordsCleanupFailure(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "4096", "8192")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "mappings", "by-key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "mappings")); err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, now: time.Now}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	g.controlLoop(ctx)
	if g.maintenance.LastError != "cleanup_failed" {
		t.Fatal("control loop ignored cleanup failure")
	}
}

func TestStorageCeilingCountsOutboxArchivesAndRecovery(t *testing.T) {
	for _, dir := range []string{"outbox/blobs", "outbox/done", "archive", "run-events", "event-state", "inbound", "tmp", "backups"} {
		t.Run(dir, func(t *testing.T) {
			root := storageQuotaFixture(t, "32768", "4096", "8192")
			ownerDir := filepath.Join(root, dir)
			if err := os.MkdirAll(ownerDir, 0700); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(ownerDir, "retained-fixture.bin")
			if err := os.WriteFile(payload, make([]byte, 65536), 0600); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewSpool(root)
			if err == nil {
				accepted, admitErr := reopened.Enqueue(Job{ID: "quota-fixture", Text: "fixture"})
				if admitErr == nil || accepted {
					t.Fatal("admission ignored owned bytes outside pending")
				}
			}
			info, err := os.Stat(payload)
			if err != nil || info.Size() != 65536 {
				t.Fatal("quota enforcement changed retained source bytes", err)
			}
		})
	}
}

func TestMaximumNextWriteUsesReserve(t *testing.T) {
	root := storageQuotaFixture(t, "65536", "8192", "16384")
	dir := filepath.Join(root, "archive")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "retained-fixture.bin"), make([]byte, 45*1024), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSpool(root)
	if err != nil {
		t.Fatal("recovery reserve must permit opening retained state", err)
	}
	if accepted, err := reopened.Enqueue(Job{ID: "next-max-fixture", Text: "fixture"}); err == nil || accepted {
		t.Fatal("admission did not reserve maximum next write and lifecycle headroom")
	}
}

func TestLiveOwnedWritesPreserveLifecycleReserve(t *testing.T) {
	root := storageQuotaFixture(t, "16384", "4096", "4096")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "inbound", "live.bin")
	if err := s.writeBlob(path, make([]byte, 4096), false); err != nil {
		t.Fatal(err)
	}
	if err := s.writeBlob(filepath.Join(root, "inbound", "second.bin"), make([]byte, 4096), false); err != nil {
		t.Fatal(err)
	}
	if err := s.writeBlob(filepath.Join(root, "inbound", "third.bin"), make([]byte, 4096), false); !errors.Is(err, storagebudget.ErrQuota) {
		t.Fatal("new ingress consumed lifecycle reserve", err)
	}
	if err := s.writeJSON(filepath.Join(root, "run-events", "completion.json"), map[string]string{"state": "completed"}); err != nil {
		t.Fatal("completion reserve unavailable", err)
	}
	before := s.storage.Snapshot()
	to := filepath.Join(root, "inbound", "renamed.bin")
	if err := s.renameOwned(path, to); err != nil {
		t.Fatal(err)
	}
	if after := s.storage.Snapshot(); before.LogicalBytes != after.LogicalBytes || before.Files != after.Files {
		t.Fatal("rename changed owned totals")
	}
	if err := s.removeOwned(to); err != nil {
		t.Fatal(err)
	}
	if err := s.writeBlob(path, make([]byte, 4096), false); err != nil {
		t.Fatal("removed capacity was not released", err)
	}
}

type oversizedStorageMediaAPI struct{ *fakeAPI }

func (a oversizedStorageMediaAPI) DownloadFile(_ context.Context, _ string, w io.Writer) error {
	_, err := io.Copy(w, bytes.NewReader(make([]byte, mediaSizeLimit+1)))
	return err
}
func TestMediaStreamingNeverExceedsReservedBytes(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{spool: s, api: oversizedStorageMediaAPI{&fakeAPI{}}}
	if path, err := g.downloadTelegramMedia(context.Background(), "fixture"); err == nil || path != "" {
		t.Fatal("oversized stream accepted")
	}
	files, err := os.ReadDir(filepath.Join(s.root, "tmp"))
	if err != nil || len(files) != 0 {
		t.Fatal("failed stream retained scratch", err)
	}
	if state := s.storage.Snapshot(); state.ReservedBytes != 0 || state.ReservedFiles != 0 {
		t.Fatal("failed stream retained reservation")
	}
}

func TestDeliveryCleanupPreservesUntrackedAndUnsettledBlobs(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := "blob-cleanup-fixture"
	if _, err := s.WriteDeliveryBlob(id, "known.txt", []byte("known")); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(id); err == nil {
		t.Fatal("missing claimed delivery completed")
	}
	if _, err := s.ReadDeliveryBlob(id, "known.txt"); err != nil {
		t.Fatal("failed completion removed unsettled blob", err)
	}
	d := Delivery{ID: id, Channel: "telegram_bot", ChatID: 11, ConversationID: "telegram-11", DeliveryTargetID: "telegram-11", Text: "fixture", Artifacts: []hubruntime.ArtifactRef{{Name: "known.txt", Blob: "known.txt"}}}
	if err := s.EnqueueDelivery(d); err != nil {
		t.Fatal(err)
	}
	if claimed, err := s.ClaimDelivery(); err != nil || claimed == nil {
		t.Fatal("claim", err)
	}
	unknown := filepath.Join(s.root, "outbox", "blobs", id, "unknown.bin")
	if err := os.WriteFile(unknown, []byte("retain unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteDelivery(id); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(unknown); err != nil || string(got) != "retain unknown" {
		t.Fatal("unknown artifact deleted", err)
	}
	if _, err := s.ReadDeliveryBlob(id, "known.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled known artifact retained", err)
	}
}

func TestStorageLimitsFailBeforeCreatingOwnerDirectories(t *testing.T) {
	for _, field := range []string{"HUB_SPOOL_MAX_BYTES", "HUB_SPOOL_MAX_OWNED_FILES", "HUB_SPOOL_MAX_NEXT_WRITE_BYTES", "HUB_SPOOL_LIFECYCLE_RESERVE_BYTES", "HUB_SPOOL_LIFECYCLE_RESERVE_FILES"} {
		t.Run(field, func(t *testing.T) {
			t.Setenv(field, "invalid-private-fixture")
			root := filepath.Join(t.TempDir(), "not-created")
			if _, err := OpenSpool(root); !errors.Is(err, storagebudget.ErrLimits) {
				t.Fatal("invalid limit accepted", err)
			}
			if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid config created directories", err)
			}
			if err := testConfig(t).Validate(); !errors.Is(err, storagebudget.ErrLimits) {
				t.Fatal("config validation ignored storage limits", err)
			}
		})
	}
}

func TestInspectAbsentSpoolIsReadOnlyAndDoesNotInitialize(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	s, err := InspectSpool(root)
	if err != nil {
		t.Fatal("absent preflight refused", err)
	}
	if _, _, err := s.StorageStatus(); err == nil {
		t.Fatal("absent owner reported available storage")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection initialized owner", err)
	}
	if err := s.SaveOffset(1); !errors.Is(err, errReadOnlySpool) {
		t.Fatal("inspection allowed write", err)
	}
	if err := s.writeBlob(filepath.Join(root, "blob"), []byte("x"), true); !errors.Is(err, errReadOnlySpool) {
		t.Fatal("inspection allowed blob", err)
	}
	if err := s.mkdirOwned(filepath.Join(root, "child")); !errors.Is(err, errReadOnlySpool) {
		t.Fatal("inspection allowed directory", err)
	}
	if _, err := s.reserveStorage(1, 1, false); !errors.Is(err, errReadOnlySpool) {
		t.Fatal("inspection allowed reservation", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection mutation created owner", err)
	}
}

func TestRoutineHookCannotBypassOwnerStorageBudget(t *testing.T) {
	root := storageQuotaFixture(t, "16384", "4096", "4096")
	s, err := OpenSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if err := s.writeBlob(filepath.Join(root, "inbound", name), make([]byte, 4096), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.rt.IntakeHook("hook-fixture", bytes.Repeat([]byte("x"), 4096), time.Now()); !errors.Is(err, storagebudget.ErrQuota) {
		t.Fatal("hook bypassed lifecycle reserve", err)
	}
	inspector, err := InspectSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspector.rt.IntakeHook("read-only-fixture", []byte("x"), time.Now()); !errors.Is(err, errReadOnlySpool) {
		t.Fatal("inspection admitted hook write", err)
	}
	if _, err := os.Stat(filepath.Join(root, "hooks", "read-only-fixture")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("inspection created hook directory", err)
	}
}
