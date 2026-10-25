package toolhub

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRefreshLeaseIndependentStores(t *testing.T) {
	seed, _, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	stores := make([]*Store, 50)
	for i := range stores {
		store, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		// Prime each process-local cache before concurrent acquisition.
		lease, acquired, err := store.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
		if err != nil || !acquired {
			t.Fatalf("prime: acquired=%v error=%v", acquired, err)
		}
		if err := store.ReleaseRefreshLease("google-work", "fixture", lease.Holder); err != nil {
			t.Fatal(err)
		}
		stores[i] = store
	}
	var wg sync.WaitGroup
	winners := make(chan int, len(stores))
	for i, store := range stores {
		wg.Go(func() {
			_, acquired, err := store.AcquireRefreshLease("google-work", "fixture", time.Hour, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			if acquired {
				winners <- i
			}
		})
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("independent stores acquired %d simultaneous refresh leases, want 1", len(winners))
	}
}

func TestRefreshLeaseReleasePreservesOtherProcess(t *testing.T) {
	seed, _, _ := seededStore(t)
	path := filepath.Join(t.TempDir(), "store.json")
	if err := seed.Save(path); err != nil {
		t.Fatal(err)
	}
	first, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, acquired, err := first.AcquireRefreshLease("google-work", "provider-a", time.Hour, time.Now())
	if err != nil || !acquired {
		t.Fatalf("first: acquired=%v error=%v", acquired, err)
	}
	b, acquired, err := second.AcquireRefreshLease("google-work", "provider-b", time.Hour, time.Now())
	if err != nil || !acquired {
		t.Fatalf("second: acquired=%v error=%v", acquired, err)
	}
	if !first.refreshLeaseHeld("google-work", "provider-b", time.Now()) {
		t.Error("joiner failed to observe other process lease")
	}
	if err := first.ReleaseRefreshLease("google-work", "provider-a", a.Holder); err != nil {
		t.Fatal(err)
	}
	current, acquired, err := first.AcquireRefreshLease("google-work", "provider-b", time.Hour, time.Now())
	if err != nil || acquired || current.Holder != b.Holder {
		t.Fatalf("release erased another provider lease: acquired=%v error=%v", acquired, err)
	}
	if err := second.ReleaseRefreshLease("google-work", "provider-b", b.Holder); err != nil {
		t.Fatal(err)
	}
	if first.refreshLeaseHeld("google-work", "provider-b", time.Now()) {
		t.Error("joiner retained released lease")
	}
}
