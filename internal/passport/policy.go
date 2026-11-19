package passport

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"github.com/letya999/hermes-hub/internal/passportview"
)

// PolicyBundle projects compiled, schema-checked passports into canonical JSON
// for the control publisher. Source layers and service credentials stay local.
func (r Revision) PolicyBundle() (contract.PolicyBundle, error) {
	eff, err := r.Effective()
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	bundle, err := passportview.PolicyProjection(eff.Header.Metadata.Space, eff.Header.Metadata.Environment, r.ID, r.Docs)
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	_, _, err = bundle.Canonical()
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	return bundle, nil
}

const policyDigestFile = "policy-digest"

func RevisionPolicyDigest(spaceDir, revision string) (string, error) {
	if revision == "" || revision == "pending" {
		return "", nil
	}
	if !filepath.IsLocal(revision) {
		return "", contract.ErrInvalid
	}
	path := filepath.Join(spaceDir, "generated", "passports", revision, policyDigestFile)
	body, err := os.ReadFile(path) // #nosec G703 -- fixed file under local revision
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(body))
	if !contract.ValidPolicyDigest(digest) {
		return "", contract.ErrInvalid
	}
	return digest, nil
}

// LinkPolicyDigest indexes the signed publication in the generated YAML cache.
// Authorization obtains the active digest from control independently.
func (r Revision) LinkPolicyDigest(spaceDir, digest string) error {
	if !filepath.IsLocal(r.ID) || r.ID == "pending" || !contract.ValidPolicyDigest(digest) {
		return contract.ErrInvalid
	}
	previous, err := RevisionPolicyDigest(spaceDir, r.ID)
	if err != nil {
		return err
	}
	if previous != "" && previous != digest {
		return contract.ErrConflict
	}
	root := filepath.Join(spaceDir, "generated", "passports")
	index := filepath.Join(root, ".policy")
	if err := os.MkdirAll(index, 0750); err != nil {
		return err
	}
	if err := atomic(filepath.Join(index, strings.TrimPrefix(digest, "sha256:")), []byte(r.ID+"\n")); err != nil {
		return err
	}
	return atomic(filepath.Join(root, r.ID, policyDigestFile), []byte(digest+"\n"))
}
