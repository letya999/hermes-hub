package main

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

// spacesRootReader answers Effective/PolicySlice from the fixture's
// published trees under a spaces root — the hubctl-test stand-in for the
// spawn contract reader (S7d).
type spacesRootReader struct{ root string }

func (r spacesRootReader) specFor(space string) (spawnv1.EffectiveSpec, error) {
	base := filepath.Join(r.root, space, "generated", "passports")
	cur, err := os.ReadFile(filepath.Join(base, "current"))
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	docs := map[string]json.RawMessage{}
	h := sha256.New()
	for _, name := range spawnv1.Names {
		path := filepath.Join(base, rev, name+".yaml")
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
		}
		body, err := os.ReadFile(path)
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
	return spawnv1.EffectiveSpec{Space: space, Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Documents: docs}, nil
}

func (r spacesRootReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return r.specFor(space)
}

func (r spacesRootReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.specFor(space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

// stubClient is the fixture-backed contract stand-in: it answers the
// effective/policy reads apply and respawn paths perform from the space's
// published tree; every other method is unused by the tested handlers and
// panics on the nil embedded client if a path ever reaches it.
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

// stubSpawnClient swaps controller resolution for the fixture-backed
// reader over the space dir's parent — the S7d contract-path stand-in for
// tests that exercise apply/respawn handlers rather than the controller
// itself.
func stubSpawnClient(t *testing.T, dir string) {
	t.Helper()
	client := stubClient{r: spacesRootReader{root: filepath.Dir(dir)}}
	prev := spawnClientFor
	spawnClientFor = func(string) (spawnv1.Client, spawnv1.Tenant, error) {
		return client, spawnv1.Tenant{Org: "local", Owner: "local"}, nil
	}
	t.Cleanup(func() { spawnClientFor = prev })
}

// hubctl tests never reach a real controller: contract resolution answers
// every space from its fixture tree (S7d). A test needing a different
// server overrides spawnClientFor/renderClient itself.
func init() {
	spawnClientFor = func(dir string) (spawnv1.Client, spawnv1.Tenant, error) {
		return stubClient{r: spacesRootReader{root: filepath.Dir(dir)}}, spawnv1.Tenant{Org: "local", Owner: filepath.Base(dir)}, nil
	}
	renderClient = func(dir string) (spawnv1.EffectiveReader, error) {
		return spacesRootReader{root: filepath.Dir(dir)}, nil
	}
}

// The contract has no disk fallback: with no controller URL and no token
// source, resolution fails loud instead of reading retired files.
func TestApplySpawnClientRefusesWithoutController(t *testing.T) {
	dir := t.TempDir() // no infra sibling, no supervisor.auth
	t.Setenv("HUB_SPAWN_CONTROLLER_URL", "")
	t.Setenv("HUB_SERVICE_TOKEN", "")
	t.Setenv("HUB_SUPERVISOR_AUTH", "")
	t.Setenv("HUB_RUNTIME_AUTH", "")
	if _, _, err := applySpawnClient(dir); err == nil {
		t.Fatal("unset controller must refuse, not fall back to files")
	}
}
