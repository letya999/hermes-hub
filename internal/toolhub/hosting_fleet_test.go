package toolhub

import (
	"context"
	"errors"
	"github.com/letya999/hermes-hub/internal/hosting"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHostingFleetConcurrentCapacityAndOwnerScope(t *testing.T) {
	f := hostingFleet{t.TempDir(), 3}
	workloads := []hosting.Workload{}
	for i := 0; i < 8; i++ {
		workloads = append(workloads, hosting.Workload{Binding: hosting.Binding{Owner: string(rune('a' + i)), Environment: "dev"}, Name: "page", Revision: 1, Release: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)})
	}
	var mu sync.Mutex
	winners := []hosting.Workload{}
	var wg sync.WaitGroup
	for _, w := range workloads {
		wg.Add(1)
		go func(w hosting.Workload) {
			defer wg.Done()
			err := f.change(context.Background(), w, false)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners = append(winners, w)
			} else if !errors.Is(err, hosting.ErrQuota) {
				t.Errorf("reservation %v", err)
			}
		}(w)
	}
	wg.Wait()
	if len(winners) != 3 {
		t.Fatalf("capacity winners %d", len(winners))
	}
	w := winners[0]
	forged := w
	forged.AdmissionDigest = "changed"
	if !errors.Is(f.change(context.Background(), forged, true), hosting.ErrDenied) {
		t.Fatal("foreign finalizer admitted")
	}
	if err := f.change(context.Background(), w, true); err != nil {
		t.Fatal(err)
	}
	if err := f.change(context.Background(), w, false); err != nil {
		t.Fatal("capacity not reclaimed")
	}
	owned, err := f.owned(context.Background(), w.Binding.Owner, "prod")
	if err != nil || len(owned) != 0 {
		t.Fatal("foreign environment listed")
	}
}
func TestHostingFleetOwnerLimitAcrossEnvironments(t *testing.T) {
	f := hostingFleet{t.TempDir(), 8}
	w := hosting.Workload{Binding: hosting.Binding{Owner: "alice", Environment: "dev"}, Name: "page", Revision: 1, Release: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Hour)}
	for i, env := range []string{"dev", "prod", "dev"} {
		w.Binding.Environment = env
		w.Revision = uint64(i + 1)
		err := f.change(context.Background(), w, false)
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 && !errors.Is(err, hosting.ErrQuota) {
			t.Fatal("owner fleet cap bypassed")
		}
	}
}
