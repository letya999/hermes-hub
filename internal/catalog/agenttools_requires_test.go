package catalog_test

import (
	"testing"

	"github.com/letya999/hermes-hub/internal/agenttools"
	"github.com/letya999/hermes-hub/internal/catalog"
)

// The agent-tools contract is a catalog surface: every declared requires
// must be a real RBAC right, equal to the right the catalog derives for the
// tool's capability id — a spec that drifts from the vocabulary fails here.
func TestHubToolsRequiresAreCatalogRights(t *testing.T) {
	docs, err := catalog.Generate()
	if err != nil {
		t.Fatal(err)
	}
	memberRights := map[string]map[string]bool{}
	for _, ts := range docs.Toolsets.Toolsets {
		for member, ops := range ts.Requires {
			set := memberRights[member]
			if set == nil {
				set = map[string]bool{}
				memberRights[member] = set
			}
			for _, op := range ops {
				set[op] = true
			}
		}
	}
	def := agenttools.HubToolsDefinition()
	for _, spec := range def.Tools {
		if len(spec.Requires) == 0 {
			t.Fatalf("tool %s declares no requires", spec.Name)
		}
		want := catalog.RequiredRight(spec.CapabilityID)
		if spec.Requires[0] != want {
			t.Fatalf("tool %s requires %q, catalog maps %s to %q", spec.Name, spec.Requires[0], spec.CapabilityID, want)
		}
		for _, right := range spec.Requires {
			if !memberRights[spec.Name][right] {
				t.Fatalf("tool %s requires %q the catalog never assigned to it", spec.Name, right)
			}
		}
	}
}
