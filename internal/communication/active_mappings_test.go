package communication

import (
	"fmt"
	"slices"
	"testing"
)

func TestActiveMappingsTrackDurableTransitionsAndReopen(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(id, status string) {
		t.Helper()
		s.mu.Lock()
		err := s.writeMappingLocked(JobMapping{JobID: id, Status: status})
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want ...string) {
		t.Helper()
		ids, err := s.activeMappingIDs()
		if err != nil || !slices.Equal(ids, want) {
			t.Fatalf("active IDs=%v want=%v err=%v", ids, want, err)
		}
	}
	write("archived", "completed")
	write("active", "running")
	check("active")
	write("pending", "queued")
	check("active", "pending")
	write("active", "cancelled")
	check("pending")
	s, err = OpenSpool(s.root)
	if err != nil {
		t.Fatal(err)
	}
	check("pending")
	write("pending", "interrupted")
	check()
}

func TestActiveMappingsConcurrentWrites(t *testing.T) {
	s, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.activeMappingIDs(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 20; i++ {
			s.mu.Lock()
			err := s.writeMappingLocked(JobMapping{JobID: fmt.Sprint(i), Status: "queued"})
			s.mu.Unlock()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		if _, err := s.activeMappingIDs(); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ids, err := s.activeMappingIDs()
	if err != nil || len(ids) != 20 {
		t.Fatalf("lost concurrent writes: count=%d err=%v", len(ids), err)
	}
}
