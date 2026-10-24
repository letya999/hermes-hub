package stack

import (
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSessionStateGrantsAttestAndUseSeparateOwnerHomes(t *testing.T) {
	roots := map[string]string{}
	for _, owner := range []string{"alice", "bob"} {
		dir := filepath.Join(t.TempDir(), owner)
		if err := os.MkdirAll(filepath.Join(dir, "hermes", "memories"), 0700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, "hermes", "memories", "USER.md")
		os.WriteFile(marker, []byte(owner+"-memory"), 0600)
		s := Settings{Schema: 3, Environment: "dev", CapabilityMode: "managed", CapabilityProfileID: owner + "-default", CapabilityGeneration: 1, User: owner, Model: "synthetic", ModelURL: "http://model-relay:8318/v1", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000, Tools: testNativeTools("todo", "memory", "session_search")}
		source, dest := filepath.Join(dir, "source.yaml"), filepath.Join(dir, "effective.yaml")
		for _, granted := range []bool{true, false} {
			if !granted {
				for _, name := range []string{"todo", "memory", "session_search"} {
					s.Tools[name] = ToolEntry{Via: ToolViaOff}
				}
			}
			body, err := yaml.Marshal(Config(s))
			if err != nil {
				t.Fatal(err)
			}
			os.WriteFile(source, body, 0600)
			opts := MaterializeOptions{Managed: true, ToolHubEndpoint: "http://toolhub:8090/mcp", ToolHubTokenEnv: "HUB_RUNTIME_AUTH", RuntimeAuthPresent: true, NativeToolsets: s.NativeCarveouts()}
			if err := MaterializeHermesConfig(source, dest, opts); err != nil {
				t.Fatal(err)
			}
			if err := ValidateManagedEffectiveConfig(dest, s, opts); err != nil {
				t.Fatal(err)
			}
			var effective struct {
				Agent struct {
					Disabled []string `yaml:"disabled_toolsets"`
				}
			}
			body, _ = os.ReadFile(dest)
			if err := yaml.Unmarshal(body, &effective); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"todo", "memory", "session_search"} {
				if slices.Contains(effective.Agent.Disabled, name) == granted {
					t.Errorf("%s grant=%v not enforced in effective Hermes config", name, granted)
				}
			}
		}
		for _, volume := range RuntimeService(s, "", dir)["volumes"].([]any) {
			if v, ok := volume.(M); ok && v["target"] == "/state/hermes" {
				roots[owner] = v["source"].(string)
			}
		}
		if body, err := os.ReadFile(marker); err != nil || string(body) != owner+"-memory" {
			t.Fatal("revocation erased or copied native memory")
		}
	}
	if roots["alice"] == "" || roots["bob"] == "" || roots["alice"] == roots["bob"] {
		t.Fatal("native memory/session state shares owner homes", roots)
	}
}
