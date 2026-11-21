package toolhub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"gopkg.in/yaml.v3"
)

// dirTreeReader is the test stand-in for the spawn contract reader: it
// answers Effective/PolicySlice from the fixture's published tree so
// fixtures that lay down passports keep working unchanged (S7d).
type dirTreeReader struct{ root string }

func (r dirTreeReader) specFor(space string) (spawnv1.EffectiveSpec, error) {
	base := filepath.Join(r.root, space, "passports")
	if _, err := os.Stat(base); err != nil {
		base = filepath.Join(r.root, space, "generated", "passports")
	}
	cur, err := os.ReadFile(filepath.Join(base, "current"))
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	pointer := ""
	if strings.HasPrefix(rev, "sha256:") {
		// Digest pointers resolve through the .policy indirection — the
		// trimmed hex is the only filesystem-safe segment (Windows forbids
		// ':' in path components).
		pointer = rev
		link, err := os.ReadFile(filepath.Join(base, ".policy", strings.TrimPrefix(rev, "sha256:")))
		if err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		rev = strings.TrimSpace(string(link))
	}
	docs := map[string]json.RawMessage{}
	h := sha256.New()
	for _, name := range spawnv1.Names {
		body, err := os.ReadFile(filepath.Join(base, rev, name+".yaml"))
		if err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		var v any
		if err := yaml.Unmarshal(body, &v); err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return spawnv1.EffectiveSpec{}, err
		}
		docs[name] = raw
		h.Write(raw)
	}
	if pointer == "" {
		pointer = "sha256:" + hex.EncodeToString(h.Sum(nil))
	}
	return spawnv1.EffectiveSpec{Space: space, Digest: pointer, Documents: docs}, nil
}

func (r dirTreeReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return r.specFor(space)
}

func (r dirTreeReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.specFor(space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

// stubClient is the fixture-backed contract stand-in for the env wiring:
// Effective/PolicySlice answer from the published tree; every other method
// panics on the nil embedded client if a path ever reaches it (S7d).
type stubClient struct {
	spawnv1.Client
	r spawnv1.EffectiveReader
}

func (c stubClient) Effective(ctx context.Context, t spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return c.r.Effective(ctx, t, space)
}

func (c stubClient) PolicySlice(ctx context.Context, t spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	return c.r.PolicySlice(ctx, t, space, service, digest)
}

// stubSpawnClient swaps the env contract resolution for the fixture-backed
// reader — the endpoint path's stand-in for HUB_SPAWN_URL (S7d).
func stubSpawnClient(t *testing.T, root string) {
	t.Helper()
	prev := spawnClientFromEnv
	spawnClientFromEnv = func() spawnv1.Client { return stubClient{r: dirTreeReader{root: root}} }
	t.Cleanup(func() { spawnClientFromEnv = prev })
}
