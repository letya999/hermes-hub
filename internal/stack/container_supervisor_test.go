package stack

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestComposeContainerSupervisorIsSinglePrivateControlService(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", CapabilityMode: "managed", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	c := Compose(s, root, dir)
	services := c["services"].(M)
	svc, ok := services["supervisor"].(M)
	if !ok {
		t.Fatal("missing container supervisor")
	}
	if _, ok := services["hermes-runtime"]; ok {
		t.Fatal("resident user runtime")
	}
	if _, ok := svc["ports"]; ok {
		t.Fatal("supervisor published host ports")
	}
	if svc["image"] != services["toolhub"].(M)["image"] || svc["read_only"] != true || svc["restart"] != "unless-stopped" {
		t.Fatalf("supervisor contract: %#v", svc)
	}
	if !reflect.DeepEqual(svc["networks"], []string{"default", sharedNetworkName, "supervisor-control"}) {
		t.Fatal(svc["networks"])
	}
	if c["networks"].(M)["supervisor-control"].(M)["internal"] != true {
		t.Fatal("control network not internal")
	}
	if svc["environment"].(M)["HUB_SUPERVISOR_HOST_SPACES"] != filepath.ToSlash(filepath.Dir(dir)) {
		t.Fatal("incorrect host bind root")
	}
	for _, raw := range svc["volumes"].([]any) {
		v := raw.(M)
		if v["target"] == "/src" {
			t.Fatal("source tree mounted")
		}
	}
	s.SupervisorURL = "http://host.docker.internal:8876"
	if _, ok := Compose(s, root, dir)["services"].(M)["supervisor"]; ok {
		t.Fatal("host selection spawned second supervisor")
	}
}

// The supervisor's opt-in authorities — administrative holds and the
// personal model — are read from its environment at startup. An operator
// provisioning them through compose env substitution must have them
// forwarded; unset they resolve empty and stay disabled.
func TestComposeSupervisorForwardsOptInAuthorities(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", CapabilityMode: "managed", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	dir := filepath.Join(t.TempDir(), "spaces", "alice")
	env := Compose(s, t.TempDir(), dir)["services"].(M)["supervisor"].(M)["environment"].(M)
	for _, key := range []string{"HUB_SUPERVISOR_ADMIN_AUTH", "HUB_PERSONAL_MODEL", "HUB_MANAGED_MODEL_URL", "OPENAI_API_KEY", "HUB_RUN_BUDGET", "HUB_STREAM_IDLE"} {
		if _, ok := env[key]; !ok {
			t.Fatalf("opt-in %s not forwarded to supervisor env", key)
		}
	}
}
