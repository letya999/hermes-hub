package stack

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

// testEffectiveReader serves the contract read path from the fixture's
// generated tree — tests keep their publish* fixtures; only the transport
// changed (S7d made the contract the consumer's read path).
type testEffectiveReader struct{ dir string }

func (r testEffectiveReader) spec(space string) (spawnv1.EffectiveSpec, error) {
	if space == "" {
		space = filepath.Base(r.dir)
	}
	dir := filepath.Join(filepath.Dir(r.dir), space)
	cur, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")) // #nosec G703 -- test fixture path
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	docs := map[string]json.RawMessage{}
	h := sha256.New()
	for _, name := range spawnv1.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml")) // #nosec G703 -- test fixture path
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

func (r testEffectiveReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return r.spec(space)
}

func (r testEffectiveReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.spec(space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

// testReader builds the shim for a fixture dir — the contract stand-in for
// what was the on-disk mirror the fixtures still lay down.
func testReader(dir string) testEffectiveReader { return testEffectiveReader{dir} }

// testLoadEffective resolves the fixture's published bundle as the contract
// would — the test-world replacement for the removed generated-file loaders.
func testLoadEffective(t *testing.T, dir string) spawnv1.Effective {
	t.Helper()
	spec, err := testReader(dir).Effective(context.Background(), spawnv1.Tenant{}, "")
	if err != nil {
		t.Fatal(err)
	}
	eff, err := spawnv1.Assemble(spec.Documents)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}
