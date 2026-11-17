package stack

import "testing"

func TestComposeSandboxRuntimeSelection(t *testing.T) {
	s := Settings{Schema: 1, User: "alice", Environment: "dev", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876"}
	runtime := RuntimeService(s, t.TempDir(), t.TempDir())
	if runtime["runtime"] != "${HUB_CONTAINER_RUNTIME:-runc}" {
		t.Fatal("Compose lacks explicit runtime selector")
	}
	services := compose(s, t.TempDir(), t.TempDir(), true)["services"].(M)
	if services["supervisor"].(M)["environment"].(M)["HUB_CONTAINER_RUNTIME"] != "${HUB_CONTAINER_RUNTIME:-runc}" {
		t.Fatal("supervisor selector not projected")
	}
	if _, ok := services["supervisor"].(M)["runtime"]; ok {
		t.Fatal("sandbox selector spread to control plane")
	}
	controller := services["workload-controller"].(M)["environment"].(M)
	if controller["HUB_CONTAINER_RUNTIME"] != "${HUB_CONTAINER_RUNTIME:-runc}" || controller["HUB_CLI_RUNTIME"] != "${HUB_CLI_RUNTIME:-}" {
		t.Fatal("cell runtime no longer inherits the operator selection")
	}
}
