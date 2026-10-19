package api

import (
	"strings"
	"testing"
)

// S5.5: the prefetch plan stays inside the 8 GB budget and only ever evicts
// images carrying hub ownership labels — foreign images are untouchable.
func TestPrefetchPlanRespectsBudget(t *testing.T) {
	target := func(name string, size int64) PrefetchTarget {
		return PrefetchTarget{Ref: "ghcr.io/x/" + name, Digest: "sha256:" + strings.Repeat(name[:1], 64), Size: size}
	}
	cachedForeign := CachedImage{Ref: "postgres:16", Size: 2 << 30, Owned: false}
	cachedStale := CachedImage{Ref: "ghcr.io/x/old", Digest: "sha256:" + strings.Repeat("0", 64), Size: 3 << 30, Owned: true}
	cachedWanted := CachedImage{Ref: "ghcr.io/x/kept", Digest: "sha256:" + strings.Repeat("k", 64), Size: 2 << 30, Owned: true}
	budget := int64(8 << 30)
	wanted := []PrefetchTarget{
		target("kept", 2<<30), // already cached — no pull
		target("newa", 3<<30), // needs 3 GiB: evict stale owned (3 GiB) to fit
	}
	pull, evict := PrefetchPlan(wanted, []CachedImage{cachedForeign, cachedStale, cachedWanted}, budget)
	if len(pull) != 1 || pull[0].Digest != wanted[1].Digest {
		t.Fatalf("pull=%+v", pull)
	}
	if len(evict) != 1 || evict[0].Ref != cachedStale.Ref {
		t.Fatalf("evict=%+v", evict)
	}
	for _, image := range evict {
		if !image.Owned {
			t.Fatalf("foreign image evicted: %+v", image)
		}
	}
}

// S5.5: when even evicting every owned image cannot fit a target, prefetch
// skips it instead of breaching the cap or touching foreign images.
func TestPrefetchPlanSkipsOversized(t *testing.T) {
	budget := int64(4 << 30)
	wanted := []PrefetchTarget{
		{Ref: "ghcr.io/x/huge", Digest: "sha256:" + strings.Repeat("h", 64), Size: 5 << 30},
		{Ref: "ghcr.io/x/small", Digest: "sha256:" + strings.Repeat("s", 64), Size: 1 << 30},
	}
	cached := []CachedImage{
		{Ref: "postgres:16", Size: 3 << 30, Owned: false},
		{Ref: "ghcr.io/x/stale", Digest: "sha256:" + strings.Repeat("0", 64), Size: 1 << 30, Owned: true},
	}
	pull, evict := PrefetchPlan(wanted, cached, budget)
	if len(pull) != 1 || pull[0].Ref != "ghcr.io/x/small" {
		t.Fatalf("pull=%+v", pull)
	}
	for _, image := range evict {
		if !image.Owned {
			t.Fatalf("foreign image evicted: %+v", image)
		}
	}
}
