package stack

import (
	"slices"
	"testing"
)

func TestComposeKeepsBrokerWitnessInIndependentEnvironmentVolume(t *testing.T) {
	for _, env := range []string{"dev", "prod"} {
		rendered := Compose(Settings{Schema: 1, User: "alice", Environment: env, Timezone: "UTC"}, "/source", "/space")
		volumes := rendered["volumes"].(M)
		recovery, ok := volumes["broker-recovery"].(M)
		if !ok || recovery["external"] != true || recovery["name"] != "hermes-credential-broker-recovery-"+env || recovery["name"] == volumes["broker-state"].(M)["name"] {
			t.Fatalf("%s: missing independent durable witness volume: %v", env, recovery)
		}
		key, ok := volumes["broker-recovery-key"].(M)
		if !ok || key["external"] != true || key["name"] != "hermes-credential-broker-recovery-key-"+env {
			t.Fatalf("%s: missing independent recovery key volume: %v", env, key)
		}
		seen := map[string]bool{}
		for name, value := range rendered["services"].(M) {
			service := value.(M)
			mounts, _ := service["volumes"].([]any)
			for _, value := range mounts {
				mount, ok := value.(M)
				if !ok {
					continue
				}
				switch mount["source"] {
				case "broker-recovery":
					if name != "credential-broker" || mount["target"] != "/var/lib/credential-broker-recovery" {
						t.Fatalf("witness volume exposed to %s or wrong path", name)
					}
				case "broker-recovery-key":
					if name != "credential-broker" || mount["target"] != "/var/lib/credential-broker-recovery-key" {
						t.Fatalf("recovery key volume exposed to %s or wrong path", name)
					}
				default:
					continue
				}
				seen[mount["source"].(string)] = true
			}
		}
		broker := rendered["services"].(M)["credential-broker"].(M)
		args := broker["entrypoint"].([]string)
		flag := slices.Index(args, "--state-witness-dir")
		if !seen["broker-recovery"] || !seen["broker-recovery-key"] || flag < 0 || flag+1 >= len(args) || args[flag+1] != "/var/lib/credential-broker-recovery" {
			t.Fatal("independent witness not mounted and selected at startup")
		}
	}
}
