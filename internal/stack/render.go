package stack

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func Config(s Settings) M {
	return managedHermesConfig(s)
}

// PolicyVersion identifies the effective runtime policy used by transport and supervisor.
func PolicyVersion(s Settings) string { return policyVersion(s) }

func policyVersion(s Settings) string {
	// Only rights-bearing settings participate (SPEC-0053 R1.1): model, ports,
	// timezone, environment and transport URLs are deployment state and must
	// not bounce bindings.
	body, _ := json.Marshal(rightsProjection(s))
	hash := sha256.Sum256(body)
	return "policy-" + hex.EncodeToString(hash[:8])
}
func Render(dir, root string) error { return RenderEnvironment(dir, root, "prod") }
