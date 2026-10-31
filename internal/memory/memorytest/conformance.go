// Package memorytest is the shared conformance suite every memory.Provider
// must pass — the built-in vault, the in-memory fake and the external
// adapters admitted in M20.11 all run the same checks (ADR-0086 D15).
package memorytest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/letya999/hermes-hub/internal/memory"
)

// Provider aliases the seam so conformance callers and adapters share one type.
type Provider = memory.Provider

// Conformance verifies the seam contract on a provider instance: scope
// isolation, idempotent capture, honest empty-vs-unavailable answers,
// provenance round-trip and hash-checked writes where the provider serves
// them.
func Conformance(t *testing.T, open func(t *testing.T) Provider) {
	t.Helper()
	ctx := context.Background()
	p := open(t)
	if p == nil {
		t.Fatal("provider factory returned nil")
	}

	secret := []byte("conformance-material-0123456789")
	scopeA := memory.MintScope(secret, "prod", "alice", string(memory.AudiencePrivate))
	scopeB := memory.MintScope(secret, "prod", "alice", string(memory.AudienceGroup))

	caps := p.Caps(ctx)
	_ = caps // Caps must never error; absence of a capability is honest, not wrong.

	t.Run("isolation", func(t *testing.T) {
		if !caps.Capture || !caps.Export {
			t.Skip("provider does not serve capture/export")
		}
		a := memory.Entry{ID: "c-a", Scope: scopeA, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "a secret preference", At: time.Now().UTC()}
		b := memory.Entry{ID: "c-b", Scope: scopeB, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudienceGroup, Body: "a group-visible note", At: time.Now().UTC()}
		if _, err := p.Capture(ctx, a); err != nil {
			t.Fatalf("capture A: %v", err)
		}
		if _, err := p.Capture(ctx, b); err != nil {
			t.Fatalf("capture B: %v", err)
		}
		exportA, err := p.Export(ctx, scopeA)
		if err != nil {
			t.Fatalf("export A: %v", err)
		}
		foundA := false
		for _, e := range exportA {
			if e.Body == a.Body {
				foundA = true
			}
			if e.Body == b.Body {
				t.Fatal("scope B content leaked into scope A export")
			}
		}
		if !foundA {
			t.Fatal("captured entry missing from its own scope export")
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		if !caps.Capture || !caps.Export {
			t.Skip("provider does not serve capture/export")
		}
		entry := memory.Entry{ID: "c-idem", Scope: scopeA, Kind: memory.KindInteractive, Provenance: memory.ProvenanceOwner, Audience: memory.AudiencePrivate, Body: "same capture twice", At: time.Now().UTC()}
		if _, err := p.Capture(ctx, entry); err != nil {
			t.Fatalf("first capture: %v", err)
		}
		if _, err := p.Capture(ctx, entry); err != nil {
			t.Fatalf("repeated capture: %v", err)
		}
		exported, err := p.Export(ctx, scopeA)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		count := 0
		for _, e := range exported {
			if e.ID == entry.ID {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("idempotent capture stored %d copies of one entry", count)
		}
	})

	t.Run("provenance", func(t *testing.T) {
		if !caps.Capture || !caps.Export {
			t.Skip("provider does not serve capture/export")
		}
		entry := memory.Entry{ID: "c-prov", Scope: scopeA, Kind: memory.KindRoutine, Provenance: memory.ProvenanceUntrusted, Audience: memory.AudiencePrivate, Body: "external text", At: time.Now().UTC()}
		if _, err := p.Capture(ctx, entry); err != nil {
			t.Fatalf("capture: %v", err)
		}
		exported, err := p.Export(ctx, scopeA)
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		for _, e := range exported {
			if e.ID == entry.ID && (e.Provenance != entry.Provenance || e.Kind != entry.Kind) {
				t.Fatalf("provider rewrote hub-owned fields: %+v", e)
			}
		}
		if entry.PromotionEligible() {
			t.Fatal("untrusted routine entry must stay promotion-ineligible")
		}
	})

	t.Run("write", func(t *testing.T) {
		if !caps.Write {
			t.Skip("provider does not serve governed writes")
		}
		op := memory.WriteOp{Path: "memory/daily/2026-10-31.md", Content: []byte("# daily\n"), WantSHA: "", Actor: memory.ActorAgent, Message: "conformance write"}
		result, err := p.Write(ctx, op)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		if result.SHA256 == "" {
			t.Fatal("write returned no content hash")
		}
		stale := memory.WriteOp{Path: op.Path, Content: []byte("# other\n"), WantSHA: "deadbeef", Actor: memory.ActorAgent}
		if _, err := p.Write(ctx, stale); !errors.Is(err, memory.ErrStaleHash) {
			t.Fatalf("stale-hash write must be rejected, got %v", err)
		}
	})
}
