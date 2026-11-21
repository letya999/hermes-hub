package supervisor

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

// specFromTree answers the contract read path from a fixture's published
// tree — the supervisor-test stand-in for the spawn effective reader.
func specFromTree(dir, space string) (spawnv1.EffectiveSpec, error) {
	base := filepath.Join(dir, "generated", "passports")
	cur, err := os.ReadFile(filepath.Join(base, "current"))
	if err != nil {
		return spawnv1.EffectiveSpec{}, spawnv1.ErrNotFound
	}
	rev := strings.TrimSpace(string(cur))
	pointer := ""
	if strings.HasPrefix(rev, "sha256:") {
		// Digest pointers resolve through the .policy indirection — the
		// trimmed hex is the only filesystem-safe segment.
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

// supTestReader reads the tree published inside one space directory.
type supTestReader struct{ dir string }

func (r supTestReader) spec() (spawnv1.EffectiveSpec, error) {
	return specFromTree(r.dir, filepath.Base(r.dir))
}

func (r supTestReader) Effective(context.Context, spawnv1.Tenant, string) (spawnv1.EffectiveSpec, error) {
	return r.spec()
}

func (r supTestReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.spec()
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}

// supRootReader resolves the published tree under <root>/<space> — the
// SpacesRoot-keyed variant for managers built without an explicit space dir.
type supRootReader struct{ root string }

func (r supRootReader) Effective(_ context.Context, _ spawnv1.Tenant, space string) (spawnv1.EffectiveSpec, error) {
	return specFromTree(filepath.Join(r.root, space), space)
}

func (r supRootReader) PolicySlice(_ context.Context, _ spawnv1.Tenant, space, service, digest string) (spawnv1.PolicySlice, error) {
	spec, err := r.Effective(context.Background(), spawnv1.Tenant{}, space)
	if err != nil {
		return spawnv1.PolicySlice{}, err
	}
	return spawnv1.PolicySlice{Space: space, Service: service, Digest: digest, Documents: spec.Documents}, nil
}
