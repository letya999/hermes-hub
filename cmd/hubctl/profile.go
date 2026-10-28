package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/letya999/hermes-hub/internal/stack"
	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

// runProfileGrants renders a provisioning profile's grants.yaml into a new
// revision of an existing capability policy: the profile's rules replace the
// policy defaults while its ceiling is carried over untouched (SPEC-0083
// R9.5). The output is a *draft* — it carries no confirmation and becomes
// authority only through `hubctl capability --kind policy --confirm`, where
// PutCapabilityPolicy rejects anything outside the ceiling.
func runProfileGrants(dir, profileName, policyPath, outPath string) error {
	rules, err := stack.ProfileGrantRulesForSpace(dir, profileName)
	if err != nil {
		return err
	}
	f, err := os.Open(policyPath) // #nosec G304 -- operator-supplied policy path
	if err != nil {
		return err
	}
	defer f.Close()
	var policy api.CapabilityPolicy
	if err := json.NewDecoder(f).Decode(&policy); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	policy.Defaults = rules
	policy.Confirmation = nil
	policy.Revision++
	body, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, append(body, '\n'), 0600)
}
