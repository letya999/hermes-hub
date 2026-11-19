package stack

import "testing"

func TestIdentityIngressCredentialStaysOutsideAgentZone(t *testing.T) {
	s := validSettings()
	s.Ingress = []string{"telegram"}
	s.ExecutionMode = "supervisor"
	s.SupervisorURL = "http://supervisor:8876"
	services := Compose(s, t.TempDir(), t.TempDir())["services"].(M)
	for name, raw := range services {
		service := raw.(M)
		environment, _ := service["environment"].(M)
		_, ingress := environment["HUB_IDENTITY_LINK_AUTH"]
		if ingress && name != "supervisor" && name != "communication-hub" {
			t.Fatalf("identity ingress credential projected into %s", name)
		}
	}
	runtime := RuntimeService(s, t.TempDir(), t.TempDir())
	environment := runtime["environment"].(M)
	for _, key := range []string{"HUB_IDENTITY_LINK_AUTH", "HUB_IDENTITY_AUTH"} {
		if _, ok := environment[key]; ok {
			t.Fatalf("agent received %s", key)
		}
	}
	for _, name := range []string{"supervisor", "communication-hub"} {
		service, ok := services[name].(M)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		if service["environment"].(M)["HUB_IDENTITY_LINK_AUTH"] != "${HUB_IDENTITY_LINK_AUTH:-}" {
			t.Fatalf("missing ingress credential on %s", name)
		}
	}
}
