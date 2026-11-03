package stack

import (
	"testing"
)

// M31.8 S4: platform_usage runs inside the runtime and needs the merged
// models.yaml limits plus the relay's reserved-usage meter — both mounted
// read-only so the agent reports consumption but never rewrites counters.
func TestRuntimeMountsModelUsageSources(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "alice", Timezone: "UTC", OAuthPort: 8000,
		CapabilityMode: "managed",
		Model:          "test", ModelURL: "http://model.invalid/v1",
		Tools: testTools("telegram")}
	// Managed spaces spawn their runtime through RuntimeService, not a
	// compose service — the mounts/env must ride that render.
	runtime := RuntimeService(s, t.TempDir(), t.TempDir())
	env := runtime["environment"].(M)
	if env["HUB_MODEL_USAGE_FILE"] != "/run/model-state/model-select.json.usage.json" {
		t.Fatalf("runtime env missing HUB_MODEL_USAGE_FILE: %v", env["HUB_MODEL_USAGE_FILE"])
	}
	if env["HUB_MODEL_REGISTRY_FILE"] != "/run/model-config/models.yaml" {
		t.Fatalf("runtime env missing HUB_MODEL_REGISTRY_FILE: %v", env["HUB_MODEL_REGISTRY_FILE"])
	}
	var registryMount, stateMount bool
	for _, raw := range runtime["volumes"].([]any) {
		mount := raw.(M)
		switch mount["target"] {
		case "/run/model-config/models.yaml":
			registryMount = mount["read_only"] == true
		case "/run/model-state":
			stateMount = mount["read_only"] == true
		}
	}
	if !registryMount || !stateMount {
		t.Fatalf("runtime must mount models.yaml and relay state read-only: %v", runtime["volumes"])
	}
}
