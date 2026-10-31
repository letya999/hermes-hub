package memorytest

import (
	"context"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Fake is an in-memory Provider. Conformance uses it as the reference
// backend; engine tests use it where a real vault on disk would be noise.
type Fake struct {
	entries map[string][]memory.Entry
}

// NewFake returns an empty in-memory provider.
func NewFake(t *testing.T) Provider {
	t.Helper()
	return &Fake{entries: map[string][]memory.Entry{}}
}

// Caps reports the capabilities the fake serves.
func (f *Fake) Caps(context.Context) memory.Caps {
	return memory.Caps{Capture: true, Export: true}
}

// Capture appends an entry under its scope, deduplicating by id.
func (f *Fake) Capture(_ context.Context, entry memory.Entry) (string, error) {
	for _, e := range f.entries[entry.Scope.Key] {
		if e.ID == entry.ID && entry.ID != "" {
			return entry.ID, nil
		}
	}
	f.entries[entry.Scope.Key] = append(f.entries[entry.Scope.Key], entry)
	return entry.ID, nil
}

// Recall reports unsupported.
func (f *Fake) Recall(_ context.Context, scope memory.Scope) (memory.Block, error) {
	return memory.Block{}, memory.ErrUnsupported
}

// Search reports unsupported.
func (f *Fake) Search(_ context.Context, scope memory.Scope, query memory.Query) ([]memory.Hit, error) {
	return nil, memory.ErrUnsupported
}

// Write reports unsupported.
func (f *Fake) Write(_ context.Context, op memory.WriteOp) (memory.WriteResult, error) {
	return memory.WriteResult{}, memory.ErrUnsupported
}

// Export returns the scope's captured entries, oldest first.
func (f *Fake) Export(_ context.Context, scope memory.Scope) ([]memory.Entry, error) {
	return append([]memory.Entry(nil), f.entries[scope.Key]...), nil
}

// Health always reports a serving fake.
func (f *Fake) Health(context.Context) error { return nil }
