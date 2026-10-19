package api

// PrefetchTarget is one digest-pinned image a T0/T1 catalog publish wants
// warm in the local cache.
type PrefetchTarget struct {
	Ref    string
	Digest string
	Size   int64
}

// CachedImage is a locally present image; Owned marks the hub's ownership
// labels, the only eviction candidates prefetch may ever consider.
type CachedImage struct {
	Ref    string
	Digest string
	Size   int64
	Owned  bool
}

// PrefetchPlan decides which targets to pull and which owned cached images
// to evict so the cache stays within budget. Foreign images are never
// eviction candidates; already-cached targets are not repulled.
func PrefetchPlan(wanted []PrefetchTarget, cached []CachedImage, budget int64) ([]PrefetchTarget, []CachedImage) {
	have := map[string]bool{}
	wantedDigest := map[string]bool{}
	for _, target := range wanted {
		wantedDigest[target.Digest] = true
	}
	var total int64
	for _, image := range cached {
		if image.Digest != "" {
			have[image.Digest] = true
		}
		total += image.Size
	}
	var pull []PrefetchTarget
	var evict []CachedImage
	for _, target := range wanted {
		if have[target.Digest] {
			continue
		}
		// Free room by evicting owned non-wanted images in cache order.
		// Foreign images and still-wanted entries are never candidates.
		for i := range cached {
			if total+target.Size <= budget {
				break
			}
			image := cached[i]
			if !image.Owned || wantedDigest[image.Digest] {
				continue
			}
			evict = append(evict, image)
			total -= image.Size
		}
		if total+target.Size > budget {
			continue
		}
		pull = append(pull, target)
		total += target.Size
	}
	return pull, evict
}
