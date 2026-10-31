package storagebudget

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testLimits() Limits {
	return Limits{Bytes: 8192, Files: 64, NextWrite: 4096, LifecycleBytes: 1024, LifecycleFiles: 4}
}
func newCounter(t *testing.T) *Counter {
	t.Helper()
	c, err := New(t.TempDir(), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func put(t *testing.T, c *Counter, path string, body []byte, life bool) error {
	t.Helper()
	return c.Write(path, int64(len(body)), life, func() error { return os.WriteFile(path, body, 0600) })
}

func TestConcurrentWritesPreserveLifecycleReserve(t *testing.T) {
	c := newCounter(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := filepath.Join(c.root, string(rune('a'+i)))
			err := put(t, c, path, make([]byte, 1024), false)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				admitted++
			} else if !errors.Is(err, ErrQuota) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if admitted != 7 || c.Snapshot().LogicalBytes != 7168 {
		t.Fatal("concurrent quota overshoot or incorrect accounting", admitted, c.Snapshot())
	}
	if err := c.CheckAdmission(); !errors.Is(err, ErrQuota) {
		t.Fatal("next-write reserve not priced")
	}
	var present string
	entries, _ := os.ReadDir(c.root)
	present = filepath.Join(c.root, entries[0].Name())
	if err := put(t, c, present, make([]byte, 512), true); err != nil {
		t.Fatal("completion reserve unavailable", err)
	}
	if c.Snapshot().LogicalBytes != 6656 {
		t.Fatal("replacement not accounted")
	}
}

func TestCrashReconciliationAndBoundedMetadataSnapshots(t *testing.T) {
	c := newCounter(t)
	dir := filepath.Join(c.root, "archive")
	if err := c.MkdirAll(dir, false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "fixture")
	if err := put(t, c, path, []byte("original"), false); err != nil {
		t.Fatal(err)
	}
	before := c.Snapshot()
	for i := 0; i < 30; i++ {
		if !c.Snapshot().ReconciledAt.Equal(before.ReconciledAt) {
			t.Fatal("metadata read walked storage")
		}
	}
	if err := os.WriteFile(filepath.Join(c.root, "crash-scratch"), make([]byte, 6000), 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(c.root, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.CheckAdmission(); !errors.Is(err, ErrQuota) {
		t.Fatal("restart ignored scratch")
	}
	if err := reopened.Remove(filepath.Join(c.root, "crash-scratch")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Rename(path, filepath.Join(dir, "renamed")); err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().LogicalBytes != 8 || reopened.Snapshot().Files != 2 {
		t.Fatal("rename/delete counters drift")
	}
	if err := reopened.Remove(filepath.Join(dir, "renamed")); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Files != 0 || reopened.Snapshot().LogicalBytes != 0 {
		t.Fatal("cleanup did not release accounting")
	}
}

func TestStreamingReservationAndAppendAccounting(t *testing.T) {
	c := newCounter(t)
	done, err := c.Reserve(4096, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reserve(4096, 2, false); !errors.Is(err, ErrQuota) {
		t.Fatal("stream reservations oversubscribed")
	}
	path := filepath.Join(c.root, "stream")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	done()
	done()
	if c.Snapshot().Complete || c.Snapshot().ReservedBytes != 0 {
		t.Fatal("stream release failed")
	}
	if err := c.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if err := c.Update(path, 1, true, func() error {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("d")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().LogicalBytes != 4 {
		t.Fatal("append delta lost")
	}
	c.Invalidate()
	if c.Snapshot().Complete {
		t.Fatal("invalidated accounting reported fresh")
	}
	if err := c.CheckAdmission(); err != nil {
		t.Fatal(err)
	}
}

func TestPathsAndErrorsDoNotExposePrivateNames(t *testing.T) {
	c := newCounter(t)
	outside := filepath.Join(t.TempDir(), "private-fixture-name")
	for _, path := range []string{outside, c.root} {
		if err := put(t, c, path, []byte("x"), true); !errors.Is(err, ErrUnsafe) {
			t.Fatal("foreign or root write allowed")
		}
	}
	secretErr := errors.New("fixture-private-name")
	err := c.Write(filepath.Join(c.root, "failed"), 1, true, func() error { return secretErr })
	if !errors.Is(err, secretErr) || !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "fixture-private-name") {
		t.Fatal("I/O errors lost class or exposed source")
	}
	if err := c.Remove(filepath.Join(c.root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing error lost")
	}
	if err := c.Rename(outside, filepath.Join(c.root, "foreign")); !errors.Is(err, ErrUnsafe) {
		t.Fatal("foreign rename allowed")
	}
	if err := c.MkdirAll(outside, false); !errors.Is(err, ErrUnsafe) {
		t.Fatal("foreign mkdir allowed")
	}
	data, _ := json.Marshal(c.Snapshot())
	if strings.Contains(string(data), c.root) || strings.Contains(string(data), "failed") {
		t.Fatal("snapshot exposes object names")
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(c.root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink privilege unavailable")
	}
	if err := c.Reconcile(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("symlink scanned as owned data")
	}
}

func TestLimitsAndFileCeiling(t *testing.T) {
	for _, l := range []Limits{{}, {Bytes: 10, Files: 1, NextWrite: 11}, {Bytes: 10, Files: 1, NextWrite: 1, LifecycleBytes: 10}, {Bytes: 10, Files: 1, NextWrite: 1, LifecycleFiles: 1}} {
		if _, err := New(t.TempDir(), l); !errors.Is(err, ErrLimits) {
			t.Fatal("invalid limits accepted")
		}
	}
	c := newCounter(t)
	if _, err := c.Reserve(-1, 0, false); !errors.Is(err, ErrLimits) {
		t.Fatal("negative reserve accepted")
	}
	if err := put(t, c, filepath.Join(c.root, "large"), make([]byte, 4097), false); !errors.Is(err, ErrLimits) {
		t.Fatal("maximum write exceeded")
	}
	l := testLimits()
	l.Files = 3
	l.LifecycleFiles = 1
	limited, err := New(t.TempDir(), l)
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, limited, filepath.Join(limited.root, "one"), []byte("x"), false); err != nil {
		t.Fatal(err)
	}
	if err := put(t, limited, filepath.Join(limited.root, "two"), []byte("x"), false); !errors.Is(err, ErrQuota) {
		t.Fatal("file and scratch reserve ignored")
	}
	if err := limited.MkdirAll(filepath.Join(limited.root, "one"), true); err == nil {
		t.Fatal("file used as directory")
	}
}

func TestAuxiliaryRootsShareCapacityAndDoNotDoubleCountChildren(t *testing.T) {
	c := newCounter(t)
	aux := t.TempDir()
	if err := c.AddRoot(aux); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(aux, "child")
	if err := c.MkdirAll(child, false); err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoot(child); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(c.root, "first")
	if err := put(t, c, first, make([]byte, 4096), false); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(child, "second")
	if err := put(t, c, second, make([]byte, 4096), false); !errors.Is(err, ErrQuota) {
		t.Fatal("auxiliary root received a second independent budget")
	}
	if err := put(t, c, second, []byte("fixture"), false); err != nil {
		t.Fatal(err)
	}
	if err := c.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if c.Snapshot().LogicalBytes != 4103 {
		t.Fatal("nested root double counted")
	}
	if err := c.Rename(second, filepath.Join(aux, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoot(filepath.Join(aux, "moved")); !errors.Is(err, ErrUnsafe) {
		t.Fatal("file registered as data root")
	}
	if err := c.Write(c.root, 0, true, func() error { return nil }); !errors.Is(err, ErrUnsafe) {
		t.Fatal("root mutation permitted")
	}
}

func TestFailedWriteReconcilesRetainedScratchAndCompletedSource(t *testing.T) {
	c := newCounter(t)
	target := filepath.Join(c.root, "source")
	if err := put(t, c, target, []byte("stable"), true); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("private injected path")
	scratch := filepath.Join(c.root, "scratch")
	err := c.Write(target, 4096, true, func() error {
		if err := os.WriteFile(scratch, make([]byte, 4096), 0600); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) || strings.Contains(err.Error(), "private") {
		t.Fatal("failed write classification", err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "stable" {
		t.Fatal("failed replacement damaged source")
	}
	if c.Snapshot().Complete {
		t.Fatal("failure reported complete accounting")
	}
	if err := c.CheckAdmission(); !errors.Is(err, ErrQuota) {
		t.Fatal("reconciliation missed crash scratch", err)
	}
	if got := c.Snapshot(); !got.Complete || got.LogicalBytes != 4102 {
		t.Fatal("incorrect reconciled bytes", got)
	}
	if err := c.Remove(scratch); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckAdmission(); err != nil {
		t.Fatal("capacity not released", err)
	}
}

func TestReconcileBoundsEntriesAndUnsafeRoots(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c", "d"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	limits := testLimits()
	limits.Files = 3
	limits.LifecycleFiles = 1
	if _, err := New(root, limits); !errors.Is(err, ErrQuota) {
		t.Fatal("unbounded entry scan", err)
	}
	if _, err := New(filepath.Join(root, "missing"), testLimits()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing root classified incorrectly", err)
	}
	if _, err := New(filepath.Join(root, "a"), testLimits()); !errors.Is(err, ErrUnsafe) {
		t.Fatal("file accepted as root", err)
	}
	c := newCounter(t)
	if err := c.AddRoot(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing auxiliary root accepted", err)
	}
	if err := c.Rename(filepath.Join(c.root, "missing"), filepath.Join(c.root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing rename classification", err)
	}
}

func TestObservedExternalChangesAreChargedBeforeReplacement(t *testing.T) {
	c := newCounter(t)
	dir := filepath.Join(c.root, "created-by-owner-recovery")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "state")
	if err := put(t, c, target, []byte("old"), true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, make([]byte, 4097), 0600); err != nil {
		t.Fatal(err)
	}
	if err := put(t, c, target, make([]byte, 4096), true); !errors.Is(err, ErrQuota) {
		t.Fatal("unobserved growth not charged before replacement", err)
	}
	if got := c.Snapshot(); got.LogicalBytes != 4097 || got.Files != 2 {
		t.Fatal("reconciled totals", got)
	}
	if err := c.Write(dir, 0, true, func() error { t.Fatal("directory reached writer"); return nil }); !errors.Is(err, ErrUnsafe) {
		t.Fatal("directory replacement accepted", err)
	}
}
