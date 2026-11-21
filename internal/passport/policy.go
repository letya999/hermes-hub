package passport

import (
	"encoding/json"

	contract "github.com/letya999/hermes-hub/contracts/identity/v1"
	spawnv1 "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"gopkg.in/yaml.v3"
)

// PolicyBundle projects compiled, schema-checked passports into canonical JSON
// for the control publisher. Source layers and service credentials stay local.
func (r Revision) PolicyBundle() (contract.PolicyBundle, error) {
	eff, err := r.Effective()
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	bundle, err := policyProjection(eff.Header.Metadata.Space, eff.Header.Metadata.Environment, r.ID, r.Docs)
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	_, _, err = bundle.Canonical()
	if err != nil {
		return contract.PolicyBundle{}, err
	}
	return bundle, nil
}

// policyProjection projects the six documents into canonical JSON — the
// digest surface the spawn store and every contract consumer share. Moved
// from internal/passportview (deleted in S7d); pure projection, no files.
func policyProjection(space, environment, revision string, docs map[string][]byte) (contract.PolicyBundle, error) {
	bundle := contract.PolicyBundle{Space: space, Environment: environment, PassportRevision: revision,
		Passports: make(map[string]json.RawMessage, len(spawnv1.Names))}
	for _, name := range spawnv1.Names {
		if _, err := spawnv1.Decode(name, docs[name]); err != nil {
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
