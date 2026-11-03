package stack

import (
	"os"
	"path/filepath"
	"testing"
)

// The shared Credential Broker lives in the infra space's Compose project, so
// its tmpfs materialized volume is named after the infra project — not the
// sibling rendering cliproxy session-init companions. A sibling's external
// volume reference must resolve to the infra project name or its init can
// never read a lease mount.
func TestSiblingCliproxyInitReadsInfraMaterializedVolume(t *testing.T) {
	root := t.TempDir()
	alice := filepath.Join(root, "spaces", "alice")
	infraSpace := filepath.Join(root, "spaces", "infraowner")
	if err := os.MkdirAll(alice, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(infraSpace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infraSpace, "agent.yaml"), []byte("infra: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alice, "agent.yaml"), []byte("infra: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"hermes-runtime": "supervised",
		"cliproxy-1":     "per-space",
	}
	rendered := Compose(s, t.TempDir(), alice)
	volumes, _ := rendered["volumes"].(M)
	mv, ok := volumes["broker-materialized"].(M)
	if !ok {
		t.Fatalf("sibling must declare the external materialized volume: %v", volumes)
	}
	if mv["name"] != "hermes-hub-infraowner-dev-broker-materialized" {
		t.Fatalf("materialized volume must resolve to the infra project: %v", mv)
	}
}
