package stack

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// S7a (M40.3): spawn-controller rides the supervisor's topology — it is
// rendered iff the supervisor service is rendered, shares the control
// image and its hardening shape, and stays reachable by toolhub and the
// supervisor on the same networks.
func TestSpawnControllerRendersIffSupervisorRenders(t *testing.T) {
	t.Setenv("HUB_RUNTIME_SUPERVISOR_URL", "")
	s := Settings{Schema: 1, Environment: "dev", User: "alice", Timezone: "UTC", ExecutionMode: "supervisor", SupervisorURL: "http://supervisor:8876", Tools: testTools("telegram"), Ingress: testIngress("telegram")}
	root := t.TempDir()
	dir := filepath.Join(root, "spaces", "alice")
	rendered := Compose(s, root, dir)
	services := rendered["services"].(M)
	spawn, ok := services["spawn-controller"].(M)
	if !ok {
		t.Fatal("spawn-controller missing beside supervisor")
	}
	supervisor := services["supervisor"].(M)
	if spawn["image"] != supervisor["image"] {
		t.Fatal("spawn-controller must run the shared control image")
	}
	// Loopback-only publication for hubctl, like the supervisor.
	if !reflect.DeepEqual(spawn["ports"], []string{"127.0.0.1:8877:8877"}) {
		t.Fatalf("spawn-controller publication must stay on loopback: %v", spawn["ports"])
	}
	// Same hardening contract: root bootstrap that setprivs to the shared
	// service uid with FOWNER only, read-only root, restart policy.
	if spawn["user"] != supervisor["user"] || !reflect.DeepEqual(spawn["cap_drop"], supervisor["cap_drop"]) ||
		!reflect.DeepEqual(spawn["cap_add"], supervisor["cap_add"]) ||
		!reflect.DeepEqual(spawn["security_opt"], supervisor["security_opt"]) ||
		spawn["read_only"] != true || spawn["restart"] != "unless-stopped" {
		t.Fatal("spawn-controller hardening must mirror the supervisor")
	}
	entrypoint := spawn["entrypoint"].([]string)
	if entrypoint[len(entrypoint)-1] != "spawn-controller" {
		t.Fatalf("entrypoint must exec the spawn-controller binary: %v", entrypoint)
	}
	if !reflect.DeepEqual(spawn["networks"], supervisor["networks"]) {
		t.Fatalf("spawn-controller must share the supervisor's networks: %v", spawn["networks"])
	}
	env := spawn["environment"].(M)
	for key, want := range map[string]string{
		"HUB_LISTEN":            "0.0.0.0:8877",
		"HUB_DATA_DIR":          "/data",
		"HUB_DATABASE_URL":      "sqlite:///data/spawn-controller.db",
		"HUB_BACKEND":           "docker",
		"HUB_SPACES_ROOT":       "/spaces",
		"HUB_IMPORT_V0_DIR":     "/spaces/.control",
		"HUB_IMPORT_SPACES_DIR": "/spaces",
		"HUB_TRUSTED":           "${HUB_TRUSTED:-true}",
		"HUB_CONTAINER_RUNTIME": "${HUB_CONTAINER_RUNTIME:-runc}",
		"HUB_RUNTIME_IMAGE":     "hermes-hub:0.5.0-dev",
		"HUB_PLATFORM_ROOT":     "/platform",
	} {
		if env[key] != want {
			t.Errorf("%s = %v, want %s", key, env[key], want)
		}
	}
	// The service token arrives only as a compose env reference — never a
	// literal in the YAML.
	token, _ := env["HUB_SERVICE_TOKEN"].(string)
	if !strings.HasPrefix(token, "${HUB_SUPERVISOR_AUTH") {
		t.Fatalf("HUB_SERVICE_TOKEN must reference supervisor.auth, got %q", token)
	}
	// The docker socket makes this service the container authority; the
	// spaces bind carries the importer inputs and — transitional for S7b —
	// is read-write so the v0 mirror can republish generated passports and
	// the settings edit can patch authored config (narrows back to
	// read-only in S7d).
	var sawSocket, sawSpaces, sawPlatform bool
	for _, raw := range spawn["volumes"].([]any) {
		v := raw.(M)
		if v["target"] == "/var/run/docker.sock" {
			sawSocket = true
		}
		if v["target"] == "/spaces" {
			sawSpaces = true
			if v["read_only"] == true {
				t.Fatal("spaces bind must stay writable while the v0 mirror is transitional")
			}
		}
		if v["target"] == "/platform/config" {
			sawPlatform = true
			if v["read_only"] != true {
				t.Fatal("platform config bind must be read-only")
			}
			wantSource := filepath.ToSlash(filepath.Join(root, "config"))
			if v["source"] != wantSource {
				t.Fatalf("platform config must bind the hub config tree: got %v, want %v", v["source"], wantSource)
			}
		}
	}
	if !sawSocket || !sawSpaces || !sawPlatform {
		t.Fatalf("spawn-controller volumes must include the docker socket, the spaces bind and the platform config bind: %v", spawn["volumes"])
	}
	// The durable store lives in a named volume this service owns.
	if _, ok := rendered["volumes"].(M)["spawn-controller-data"]; !ok {
		t.Fatal("spawn-controller-data named volume missing")
	}
	// Consumers get the spawn URL and the same token reference.
	for _, name := range []string{"toolhub", "supervisor"} {
		consumer, ok := services[name].(M)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		e := consumer["environment"].(M)
		if e["HUB_SPAWN_URL"] != "http://spawn-controller:8877" {
			t.Errorf("%s HUB_SPAWN_URL = %v", name, e["HUB_SPAWN_URL"])
		}
		tok, _ := e["HUB_SPAWN_TOKEN"].(string)
		if !strings.HasPrefix(tok, "${HUB_SUPERVISOR_AUTH") {
			t.Errorf("%s HUB_SPAWN_TOKEN must reference supervisor.auth, got %q", name, tok)
		}
	}
	// No supervisor → no spawn-controller.
	s.SupervisorURL = "http://host.docker.internal:8876"
	if _, ok := Compose(s, root, dir)["services"].(M)["spawn-controller"]; ok {
		t.Fatal("spawn-controller rendered without the supervisor")
	}
	if _, ok := Compose(s, root, dir)["volumes"].(M)["spawn-controller-data"]; ok {
		t.Fatal("spawn volume rendered without the service")
	}
}

// The stack passport bounds the topology: a plan that declares the
// supervisor carries its spawn-controller companion; one that does not
// drops the service even if a render produced it.
func TestSpawnControllerFollowsSupervisorPlan(t *testing.T) {
	s := managedPassportSibling()
	s.PassportServices = map[string]string{
		"hermes-runtime": "supervised",
		"toolhub":        "shared",
		"supervisor":     "shared",
	}
	s.Infra = nil // infra default renders the shared stack
	plan := newComposePlan(s)
	if _, ok := plan.local["spawn-controller"]; !ok {
		t.Fatal("a declared shared supervisor must carry spawn-controller in the plan")
	}
	got := plan.filter(M{"supervisor": M{}, "spawn-controller": M{}})
	if _, ok := got["spawn-controller"]; !ok {
		t.Fatal("spawn-controller must survive the plan filter beside the supervisor")
	}
	s.Infra = nil
	s.PassportServices = map[string]string{"hermes-runtime": "supervised", "toolhub": "shared"}
	plan = newComposePlan(s)
	got = plan.filter(M{"supervisor": M{}, "spawn-controller": M{}})
	if _, ok := got["spawn-controller"]; ok {
		t.Fatal("spawn-controller without a declared supervisor must be dropped")
	}
}
