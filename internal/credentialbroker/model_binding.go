package credentialbroker

import (
	"crypto/sha256"
	"fmt"
)

// ModelRelayBinding keeps dedicated broker workloads distinct across owners.
func ModelRelayBinding(owner, service string) string {
	digest := sha256.Sum256([]byte(owner + "\x00" + service))
	return fmt.Sprintf("model-relay-%x", digest[:12])
}

// MediaBinding keeps retired and replacement infra owners out of one workload.
func MediaBinding(owner string) string {
	digest := sha256.Sum256([]byte(owner))
	return fmt.Sprintf("hub-media-%x", digest[:12])
}

// CliproxySessionBinding pins one OAuth session credential to a workload
// slot of its cliproxy instance. The broker admits one dedicated credential
// per binding, so a multi-session instance needs a distinct slot per ref; a
// session named after its instance keeps the legacy unsuffixed slot so
// existing grants stay valid.
func CliproxySessionBinding(instance, ref string) string {
	if ref == instance {
		return instance
	}
	return instance + "-" + ref
}

// CliproxyKeyBinding is the same slot rule for proxy_access client keys:
// the conventional "cliproxy" ref keeps the legacy <instance>-key slot.
func CliproxyKeyBinding(instance, ref string) string {
	if ref == "cliproxy" {
		return instance + "-key"
	}
	return instance + "-key-" + ref
}
