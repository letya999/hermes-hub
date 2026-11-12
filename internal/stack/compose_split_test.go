package stack

import "testing"

func TestSplitInfraComposeFollowsPassportAndPreservesModelGrantVolume(t *testing.T) {
	infra := true
	s := Settings{User: "alice", Environment: "dev", Infra: &infra,
		PassportServices: map[string]string{
			"credential-broker": "shared", "toolhub": "shared", "cliproxy": "shared",
			"model-relay": "per-space", "egress-relay": "per-space", "cliproxy-alice": "per-space",
		}}
	full := M{
		"name": "hermes-hub-alice-dev",
		"services": M{
			"credential-broker": M{}, "toolhub": M{}, "cliproxy": M{},
			"model-relay": M{}, "egress-relay": M{},
			"cliproxy-alice": M{}, "cliproxy-alice-init": M{},
		},
		"volumes":  M{"broker-secrets-model": M{}},
		"networks": M{managedAgentNetwork(s): M{"name": managedAgentNetwork(s), "internal": true}},
	}
	shared, user := SplitInfraCompose(s, full)
	if shared["name"] != "hermes-hub-shared-dev" || user["name"] != "hermes-hub-alice-dev-user" {
		t.Fatalf("project split: shared=%v user=%v", shared["name"], user["name"])
	}
	for _, name := range []string{"credential-broker", "toolhub", "cliproxy"} {
		if shared["services"].(M)[name] == nil || user["services"].(M)[name] != nil {
			t.Fatalf("shared service %s assigned to wrong project", name)
		}
	}
	for _, name := range []string{"model-relay", "egress-relay", "cliproxy-alice", "cliproxy-alice-init"} {
		if user["services"].(M)[name] == nil || shared["services"].(M)[name] != nil {
			t.Fatalf("per-space service %s assigned to wrong project", name)
		}
	}
	if got := user["volumes"].(M)["broker-secrets-model"].(M)["name"]; got != "hermes-hub-alice-dev_broker-secrets-model" {
		t.Fatalf("model grant volume changed: %v", got)
	}
	if got := user["networks"].(M)[managedAgentNetwork(s)].(M)["external"]; got != true {
		t.Fatalf("per-user agent network is not external: %v", got)
	}
	if full["services"].(M)["model-relay"] == nil {
		t.Fatal("split mutated the original compose plan")
	}
}

func TestSplitInfraComposeKeepsSharedOnlyOwnerInOneProject(t *testing.T) {
	infra := true
	s := Settings{Infra: &infra, Environment: "dev", PassportServices: map[string]string{"toolhub": "shared"}}
	full := M{"name": "hermes-hub-alice-dev", "services": M{"toolhub": M{}}, "volumes": M{"toolhub-data": M{}}}
	shared, user := SplitInfraCompose(s, full)
	if user != nil || shared["name"] != SharedProjectName("dev") {
		t.Fatalf("shared-only owner produced an extra project: shared=%v user=%v", shared["name"], user)
	}
	if got := shared["volumes"].(M)["toolhub-data"].(M)["name"]; got != "hermes-hub-alice-dev_toolhub-data" {
		t.Fatalf("shared volume must pin the pre-split name: %v", got)
	}
}
