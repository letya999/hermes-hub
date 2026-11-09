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
