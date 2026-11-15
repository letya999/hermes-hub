package memorytest

import (
	"context"
	"errors"
	"testing"

	"github.com/letya999/hermes-hub/internal/memory"
)

func TestConformanceFake(t *testing.T) {
	Conformance(t, func(t *testing.T) Provider { return NewFake(t) })
}

// A provider advertising no capabilities must still pass Conformance — the
// suite skips lanes it cannot exercise rather than demanding them.
func TestConformanceLimitedProvider(t *testing.T) {
	Conformance(t, func(t *testing.T) Provider { return nullProvider{} })
}

type nullProvider struct{}

func (nullProvider) Caps(context.Context) memory.Caps { return memory.Caps{} }
func (nullProvider) Capture(context.Context, memory.Entry) (string, error) {
	return "", memory.ErrUnsupported
}
func (nullProvider) Recall(context.Context, memory.Scope) (memory.Block, error) {
	return memory.Block{}, memory.ErrUnsupported
}
func (nullProvider) Search(context.Context, memory.Scope, memory.Query) ([]memory.Hit, error) {
	return nil, memory.ErrUnsupported
}
func (nullProvider) Write(context.Context, memory.WriteOp) (memory.WriteResult, error) {
	return memory.WriteResult{}, memory.ErrUnsupported
}
func (nullProvider) Export(context.Context, memory.Scope) ([]memory.Entry, error) {
	return nil, memory.ErrUnsupported
}
func (nullProvider) Health(context.Context) error { return nil }

// The fake serves only capture and export — every other lane reports
// ErrUnsupported honestly rather than faking results.
func TestFakeUnsupportedLanes(t *testing.T) {
	f := NewFake(t)
	ctx := context.Background()
	scope := memory.MintScope([]byte("m"), "prod", "alice", "private")
	if _, err := f.Recall(ctx, scope); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Recall must report ErrUnsupported, got %v", err)
	}
	if _, err := f.Search(ctx, scope, memory.Query{}); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Search must report ErrUnsupported, got %v", err)
	}
	if _, err := f.Write(ctx, memory.WriteOp{}); !errors.Is(err, memory.ErrUnsupported) {
		t.Fatalf("Write must report ErrUnsupported, got %v", err)
	}
	if err := f.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
}
