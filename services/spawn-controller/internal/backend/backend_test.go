package backend

import (
	"context"
	"sync"
	"testing"
)

// Handler goroutines list and inspect while spawns write the map: run under
// -race the unlocked reads were a fatal concurrent map access.
func TestBackendMapAccessIsSynchronized(t *testing.T) {
	b := &processBackend{children: map[string]*childRuntime{}}
	for i := range 4 {
		done := make(chan struct{})
		close(done)
		key := "rt-" + string(rune('a'+i))
		b.children[key] = &childRuntime{rt: Runtime{ID: key, State: "running"}, done: done}
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_, _ = b.List(context.Background())
				_, _ = b.Inspect(context.Background(), "rt-a")
				_ = b.Stop(context.Background(), "rt-nothing")
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 50 {
			key := "rt-x" + string(rune('a'+i%3))
			done := make(chan struct{})
			close(done)
			b.mu.Lock()
			b.children[key] = &childRuntime{rt: Runtime{ID: key}, done: done}
			b.mu.Unlock()
			b.remove(key)
		}
	}()
	wg.Wait()
}
