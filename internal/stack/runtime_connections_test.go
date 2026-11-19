package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeConnectionWiring(t *testing.T) {
	root := t.TempDir()
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC"}
	if got := RuntimeConnections(s, root); len(got) != 0 {
		t.Fatalf("disabled connections mounted: %d", len(got))
	}
	if _, present := RuntimeService(s, root, root)["environment"].(M)["HUB_EXPOSURE_CONFIG"]; present {
		t.Fatal("disabled exposure reached runtime env")
	}
	s.Tools = map[string]ToolEntry{"ssh": {Via: ToolViaToolHub}, "exposure": {Via: ToolViaToolHub}}
	connections := RuntimeConnections(s, root)
	if len(connections) != 2 || connections[0].Name != "ssh" || connections[1].Name != "exposure" {
		t.Fatal("connection spec lost enabled features")
	}
	service := RuntimeService(s, root, root)
	env := service["environment"].(M)
	volumes := service["volumes"].([]any)
	for _, connection := range connections {
		if env[connection.EnvKey] != connection.ConfigPath {
			t.Fatalf("%s runtime env diverged", connection.Name)
		}
		found := false
		for _, item := range volumes {
			mount := item.(M)
			if mount["target"] == connection.Target {
				found = mount["source"] == filepath.ToSlash(connection.Source) && mount["read_only"] == true
			}
		}
		if !found {
			t.Fatalf("%s runtime mount diverged", connection.Name)
		}
	}
}

func TestValidateRuntimeConnectionsMissingAndDisabled(t *testing.T) {
	dir := t.TempDir()
	s := Settings{Tools: map[string]ToolEntry{}, PassportServices: map[string]string{"egress-relay": "per-space"}}
	if err := ValidateRuntimeConnections(s, dir); err != nil {
		t.Fatalf("disabled feature required config: %v", err)
	}
	s.Tools["exposure"] = ToolEntry{Via: ToolViaToolHub}
	if err := ValidateRuntimeConnections(s, dir); err == nil {
		t.Fatal("missing exposure config admitted")
	}
	writeExposureConfig(t, dir)
	if err := ValidateRuntimeConnections(s, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "connections", "exposure", "sish-key.pem")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimeConnections(s, dir); err == nil {
		t.Fatal("missing file credential admitted")
	}
}

func TestManagedSishExposureRequiresDeclaredEgressRelay(t *testing.T) {
	dir := t.TempDir()
	writeExposureConfig(t, dir)
	s := Settings{Tools: map[string]ToolEntry{"exposure": {Via: ToolViaToolHub}}, PassportServices: map[string]string{"hermes-runtime": "supervised"}}
	if err := ValidateRuntimeConnections(s, dir); err == nil {
		t.Fatal("managed sish exposure admitted without a declared egress relay")
	}
	s.PassportServices["egress-relay"] = "per-space"
	if err := ValidateRuntimeConnections(s, dir); err != nil {
		t.Fatal(err)
	}
}
