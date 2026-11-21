//go:build integration

package devcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/internal/passport"

	"gopkg.in/yaml.v3"
)

// publishGenerated lays down the generated/passports layout for a compiled
// fixture revision — the on-disk stand-in for a submitted+activated revision
// the harness's real consumers (RenderEnvironment/ReadEffective) read through
// the contract reader.
func publishGenerated(dir string, rev passport.Revision) error {
	root := filepath.Join(dir, "generated", "passports")
	revDir := filepath.Join(root, rev.ID)
	if err := os.MkdirAll(revDir, 0o750); err != nil {
		return err
	}
	for name, body := range rev.Docs {
		if err := os.WriteFile(filepath.Join(revDir, name+".yaml"), body, 0o600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(root, "current"), []byte(rev.ID+"\n"), 0o600)
}

// generatedReader serves the contract read path from a fixture's generated
// tree — the same shape effective_shim_test.go carries for unit tests.
type generatedReader struct{ dir string }

func (r generatedReader) spec(space string) (spawnv1.EffectiveSpec, error) {
	if space == "" {
		space = filepath.Base(r.dir)
	}
	dir := filepath.Join(filepath.Dir(r.dir), space)
	cur, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")) // #nosec G703 -- fixture path
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	docs := map[string]json.RawMessage{}
	h := sha256.New()
	for _, name := range spawnv1.Names {
		body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", rev, name+".yaml")) // #nosec G703 -- fixture path
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

func (r generatedReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return r.spec(space)
}

func (r generatedReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.spec(space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

var _ spawnv1.EffectiveReader = generatedReader{}

// generatedRevisionID reads the fixture's current pointer — the stand-in for
// the removed passport.Current over the same on-disk layout.
func generatedRevisionID(dir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, "generated", "passports", "current")) // #nosec G703 -- fixture path
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}
