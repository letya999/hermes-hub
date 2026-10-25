package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// prefetchCacheBudget caps the warm connector-image cache — the same 8 GB
// ceiling the build cache already keeps.
const prefetchCacheBudget = 8 << 30

// prefetchRunner is the docker CLI seam; tests substitute it to observe the
// dispatch without a daemon.
var prefetchRunner = dockerOutput

// prefetchRunner is used by the `connector prefetch` command dispatch.

// connectorPrefetch warms every digest-pinned catalog image into the local
// cache. Foreign images are never eviction candidates; a superseded digest of
// a catalog-owned repository is the only thing the plan may drop.
func connectorPrefetch(ctx context.Context, run func(context.Context, ...string) ([]byte, error)) error {
	entries, err := api.PreparedCatalog()
	if err != nil {
		return err
	}
	var wanted []api.PrefetchTarget
	ownedRepos := map[string]bool{}
	for _, entry := range entries {
		if entry.Image == nil || entry.Image.Digest == "" {
			continue
		}
		size, err := prefetchImageSize(ctx, run, entry.Image.Ref)
		if err != nil {
			return err
		}
		wanted = append(wanted, api.PrefetchTarget{Ref: entry.Image.Ref, Digest: entry.Image.Digest, Size: size})
		ownedRepos[strings.SplitN(entry.Image.Ref, "@", 2)[0]] = true
	}
	cached, err := prefetchCachedImages(ctx, run, ownedRepos)
	if err != nil {
		return err
	}
	pull, evict := api.PrefetchPlan(wanted, cached, prefetchCacheBudget)
	for _, target := range pull {
		if _, err := run(ctx, "pull", target.Ref); err != nil {
			return fmt.Errorf("prefetch pull %s: %w", target.Ref, err)
		}
	}
	for _, image := range evict {
		if _, err := run(ctx, "rmi", image.Ref); err != nil {
			return fmt.Errorf("prefetch evict %s: %w", image.Ref, err)
		}
	}
	fmt.Printf("prefetch: %d pull, %d evict (budget %d GiB)\n", len(pull), len(evict), prefetchCacheBudget>>30)
	return nil
}

// prefetchImageSize resolves the compressed layer sum of a schema-2 manifest.
func prefetchImageSize(ctx context.Context, run func(context.Context, ...string) ([]byte, error), ref string) (int64, error) {
	body, err := run(ctx, "manifest", "inspect", ref)
	if err != nil {
		return 0, fmt.Errorf("prefetch manifest %s: %w", ref, err)
	}
	var manifest struct {
		Config struct{ Size int64 }   `json:"config"`
		Layers []struct{ Size int64 } `json:"layers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return 0, fmt.Errorf("prefetch manifest %s: %w", ref, err)
	}
	var total int64 = manifest.Config.Size
	for _, layer := range manifest.Layers {
		total += layer.Size
	}
	return total, nil
}

// prefetchCachedImages enumerates the local cache; an image counts as owned
// only when its repository is one the catalog itself pins.
func prefetchCachedImages(ctx context.Context, run func(context.Context, ...string) ([]byte, error), ownedRepos map[string]bool) ([]api.CachedImage, error) {
	body, err := run(ctx, "images", "--digests", "--format", "{{.Repository}}|{{.Tag}}|{{.Digest}}|{{.Size}}")
	if err != nil {
		return nil, fmt.Errorf("prefetch image list: %w", err)
	}
	var cached []api.CachedImage
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) != 4 {
			continue
		}
		cached = append(cached, api.CachedImage{
			Ref:    parts[0] + ":" + parts[1],
			Digest: parts[2],
			Size:   parseDockerSize(parts[3]),
			Owned:  ownedRepos[parts[0]],
		})
	}
	return cached, nil
}

// parseDockerSize reads `docker images` human sizes (1.2GB, 512MB, 75.5kB).
func parseDockerSize(value string) int64 {
	value = strings.TrimSpace(value)
	for _, suffix := range []string{"GB", "MB", "kB", "B"} {
		if !strings.HasSuffix(value, suffix) {
			continue
		}
		number, err := strconv.ParseFloat(strings.TrimSuffix(value, suffix), 64)
		if err != nil {
			return 0
		}
		multiplier := map[string]float64{"GB": 1 << 30, "MB": 1 << 20, "kB": 1 << 10, "B": 1}[suffix]
		return int64(number * multiplier)
	}
	return 0
}
