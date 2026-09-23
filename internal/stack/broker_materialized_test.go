package stack

import "testing"

func TestComposeSharesDedicatedBrokerLeaseVolume(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "prod", Timezone: "UTC"}
	stack := Compose(s, "/source", "/space")
	volume := stack["volumes"].(M)["broker-materialized"].(M)
	if volume["name"] != "hermes-hub-alice-prod-broker-materialized" || volume["driver_opts"].(M)["type"] != "tmpfs" {
		t.Fatalf("Broker lease volume is not owner-scoped tmpfs: %v", volume)
	}
	services := stack["services"].(M)
	for _, name := range []string{"credential-broker", "toolhub", "workload-controller"} {
		service := services[name].(M)
		found := false
		for _, item := range service["volumes"].([]any) {
			mount := item.(M)
			if mount["target"] == "/run/broker-materialized" && mount["source"] == "broker-materialized" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s lacks Broker lease volume", name)
		}
	}
	for _, name := range []string{"toolhub", "workload-controller"} {
		if got := services[name].(M)["environment"].(M)["HUB_BROKER_MATERIALIZED_VOLUME"]; got != volume["name"] {
			t.Fatalf("%s resolves wrong Docker volume: %v", name, got)
		}
	}
}
