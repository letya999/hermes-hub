package toolhub

import (
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/identity"
)

// #515: no vault plane at all still answers honestly — memory_search never
// hard-errors on the model surface and never falls back to a shared store.
func TestMemorySearchWithoutVaultResolverReportsUnavailable(t *testing.T) {
	g := &Gateway{}
	out, err := g.memorySearch(t.Context(), identity.Envelope{PrincipalID: "alice", Environment: "dev"}, map[string]any{"query": "anything"})
	if err != nil {
		t.Fatalf("memorySearch errored: %v", err)
	}
	if out["status"] != "unavailable" {
		t.Fatalf("status = %v, want unavailable", out["status"])
	}
	if hits, ok := out["hits"].([]map[string]any); !ok || len(hits) != 0 {
		t.Fatalf("unavailable must carry empty hits: %v", out["hits"])
	}
}

func TestMemorySearchExcerptBoundsPreview(t *testing.T) {
	long := strings.Repeat("x", 300)
	got := memorySearchExcerpt(long)
	if len([]rune(got)) != 241 || !strings.HasSuffix(got, "…") {
		t.Fatalf("excerpt not bounded: %d runes", len([]rune(got)))
	}
	if memorySearchExcerpt(" short ") != "short" {
		t.Fatal("excerpt must trim and pass short text through")
	}
}
