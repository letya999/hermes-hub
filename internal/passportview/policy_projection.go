package passportview

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	"gopkg.in/yaml.v3"
)

func PolicyProjection(space, environment, revision string, docs map[string][]byte) (contract.PolicyBundle, error) {
	bundle := contract.PolicyBundle{Space: space, Environment: environment, PassportRevision: revision,
		Passports: make(map[string]json.RawMessage, len(Names))}
	for _, name := range Names {
		if _, err := Decode(name, docs[name]); err != nil {
			return contract.PolicyBundle{}, err
		}
		var document map[string]any
		if err := yaml.Unmarshal(docs[name], &document); err != nil {
			return contract.PolicyBundle{}, err
		}
		body, err := json.Marshal(document)
		if err != nil {
			return contract.PolicyBundle{}, err
		}
		bundle.Passports[name] = body
	}
	return bundle, nil
}

func MatchesPolicyPointer(spaceDir, digest string) bool {
	current, err := CurrentPolicyDigestGenerated(filepath.Join(spaceDir, "generated"))
	return err == nil && current == digest
}

// CurrentPolicyDigestGenerated reads the signed-publication pointer from a
// generated tree. Revision-only pointers cannot authorize model services.
func CurrentPolicyDigestGenerated(dir string) (string, error) {
	body, err := os.ReadFile(filepath.Join(dir, "passports", "current")) // #nosec G304 -- operator-owned generated tree and fixed relative path
	if err != nil {
		return "", contract.ErrUnavailable
	}
	digest := strings.TrimSpace(string(body))
	if !contract.ValidPolicyDigest(digest) {
		return "", contract.ErrForbidden
	}
	return digest, nil
}

// VerifyPolicyProjection attests the rendered source against the already
// verified control publication before its configuration can drive a run.
func VerifyPolicyProjection(spaceDir string, policy contract.PolicyBundle) error {
	if !MatchesPolicyPointer(spaceDir, policy.PassportDigest) {
		return contract.ErrForbidden
	}
	revision, err := Current(spaceDir)
	if err != nil || revision != policy.PassportRevision {
		return contract.ErrForbidden
	}
	const maxBytes = 512 << 10
	docs := make(map[string][]byte, len(Names))
	remaining := maxBytes
	for _, name := range Names {
		path := filepath.Join(spaceDir, "generated", "passports", revision, name+".yaml")
		file, err := os.Open(path) // #nosec G304 G703 -- validated local revision and fixed filenames
		if err != nil {
			return contract.ErrUnavailable
		}
		body, err := io.ReadAll(io.LimitReader(file, int64(remaining)+1))
		_ = file.Close()
		remaining -= len(body)
		if err != nil || remaining < 0 {
			return contract.ErrInvalid
		}
		docs[name] = body
	}
	projected, err := PolicyProjection(policy.Space, policy.Environment, revision, docs)
	if err != nil {
		return err
	}
	_, digest, err := projected.Canonical()
	if err != nil || digest != policy.PassportDigest || !MatchesPolicyPointer(spaceDir, digest) {
		return contract.ErrForbidden
	}
	return nil
}
