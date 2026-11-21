package migration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"gopkg.in/yaml.v3"
)

// migTestReader answers the contract read path from the fixture's published
// tree — the stand-in EffectiveReader for pre-S7d fixtures (same shape as
// the stack/toolhub shims).
type migTestReader struct{ dir string }

func (r migTestReader) spec() (spawnv1.EffectiveSpec, error) {
	base := filepath.Join(r.dir, "generated", "passports")
	cur, err := os.ReadFile(filepath.Join(base, "current"))
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
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
	return spawnv1.EffectiveSpec{Space: filepath.Base(r.dir), Digest: "sha256:" + hex.EncodeToString(h.Sum(nil)), Documents: docs}, nil
}

func (r migTestReader) Effective(context.Context, spawnv1.Tenant, string) (spawnv1.EffectiveSpec, error) {
	return r.spec()
}

func (r migTestReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.spec()
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}
