package fake_test

import (
	"net/http"
	"testing"

	spawn "github.com/letya999/hermes-hub/contracts/spawn/v1"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/conformance"
	"github.com/letya999/hermes-hub/contracts/spawn/v1/fake"
)

func TestFakePassesConformance(t *testing.T) {
	server := &fake.Server{Services: []string{"compose"}}
	conformance.Both(t, server,
		func(token string) http.Handler { return spawn.Handler(server, token) },
		conformance.Fixture{
			Tenant:   spawn.Tenant{Org: "org-a", Owner: "alice"},
			Foreign:  spawn.Tenant{Org: "org-b", Owner: "mallory"},
			Space:    "alice",
			Consumer: "compose",
			Layers: map[string]map[string]map[string]any{
				"platform-ceiling":  {"stack": {"services": map[string]any{"hermes-runtime": map[string]any{"mode": "supervised"}}}},
				"platform-template": {"config": {"runtime": map[string]any{"timezone": "UTC"}}},
				"org-ceiling":       {"grants": {"allow": []any{"workspace"}}},
				"org-template":      {"workspace": {"writable": []any{"/workspace"}}},
				"user":              {"config": {"memory": map[string]any{"backend": "local"}}},
			},
		})
}
