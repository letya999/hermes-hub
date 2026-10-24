package stack

import "testing"

// #287: per-call credential material (credentials.env, <name>.secret) is
// staged on a dedicated tmpfs volume shared by ToolHub and the workload
// controller. The broker never sees it; the persistent state bind must not
// carry it either.
func TestComposeSharesCredentialStagingVolume(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "prod", Timezone: "UTC"}
	stack := Compose(s, "/source", "/space")
	volume, ok := stack["volumes"].(M)["cred-staging"].(M)
	if !ok || volume["name"] != "hermes-hub-alice-prod-cred-staging" || volume["driver_opts"].(M)["type"] != "tmpfs" {
		t.Fatalf("credential staging volume is not owner-scoped tmpfs: %v", stack["volumes"].(M)["cred-staging"])
	}
	services := stack["services"].(M)
	for _, name := range []string{"toolhub", "workload-controller"} {
		service := services[name].(M)
		found := false
		for _, item := range service["volumes"].([]any) {
			mount := item.(M)
			if mount["target"] == "/run/cred-staging" && mount["source"] == "cred-staging" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s lacks credential staging volume", name)
		}
		env := service["environment"].(M)
		if env["HUB_CRED_STAGING"] != "/run/cred-staging" || env["HUB_CRED_STAGING_VOLUME"] != volume["name"] {
			t.Fatalf("%s staging env HUB_CRED_STAGING=%v HUB_CRED_STAGING_VOLUME=%v", name, env["HUB_CRED_STAGING"], env["HUB_CRED_STAGING_VOLUME"])
		}
	}
	// The broker and unrelated services never see workload credential material.
	for _, item := range services["credential-broker"].(M)["volumes"].([]any) {
		if item.(M)["target"] == "/run/cred-staging" {
			t.Fatal("credential-broker mounts workload credential staging")
		}
	}
}
