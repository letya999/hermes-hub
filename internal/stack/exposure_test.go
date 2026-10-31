package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeExposureConfig(t *testing.T, dir string) {
	t.Helper()
	cfgDir := filepath.Join(dir, "connections", "exposure")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		t.Fatal(err)
	}
	body := `schema: 1
provider: sish
relay:
  host: tunnel.example.com
  user: alice
  domain: hub.example.com
  host_keys:
    - "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEa6/V1v39RJXNZvJSJp7hEDWdTsFc9n/k8G+sMFBqGX"
zone: alice
key_ref: "file:sish-key.pem"
endpoints:
  onboarding:
    target: hub_public_listener
`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExposureFeatureWiring(t *testing.T) {
	tools, ingress, err := LegacyFeatureTools([]string{"exposure"})
	if err != nil {
		t.Fatalf("legacy mapping: %v", err)
	}
	if tools["exposure"].Via != ToolViaToolHub || len(ingress) != 0 {
		t.Fatalf("exposure must be a toolhub capability family: %v %v", tools, ingress)
	}
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC", BrowserPort: 6080, OAuthPort: 8000}
	s.Tools, s.Ingress = testTools("exposure"), testIngress("exposure")
	if err := s.Validate(); err != nil {
		t.Fatalf("exposure alone should validate: %v", err)
	}
	if !s.Has("exposure") {
		t.Fatal("Has(exposure) must reflect the tools entry")
	}
}

func TestExposureReadOnlyCap(t *testing.T) {
	s := Settings{Schema: 1, Environment: "prod", User: "me", Timezone: "UTC",
		Tools: map[string]ToolEntry{"exposure": {Via: ToolViaToolHub, Access: ToolAccessRO}}}
	if got := s.readOnlyCapabilities(); !slices.Contains(got, "exposure") {
		t.Fatalf("access ro must cap the exposure capability: %v", got)
	}
}

func TestExposureRenderMountsConfigAndWiresEnv(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	writeExposureConfig(t, d)
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.Tools, s.Ingress = testTools("exposure"), testIngress("exposure")
	if err = WriteSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err = RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(d, "generated", "compose.prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(compose)
	if !strings.Contains(text, "target: /state/exposure") || !strings.Contains(text, "read_only: true") {
		t.Fatal("exposure config mount missing or writable")
	}
	hermes, err := os.ReadFile(filepath.Join(d, "generated", "hermes.prod.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var rendered map[string]any
	if err = yaml.Unmarshal(hermes, &rendered); err != nil {
		t.Fatal(err)
	}
	hub := rendered["mcp_servers"].(map[string]any)["hub"].(map[string]any)
	env := hub["env"].(map[string]any)
	if env["HUB_EXPOSURE_CONFIG"] != "/state/exposure/config.yaml" {
		t.Fatalf("exposure env wrong: %v", env)
	}
}

func TestExposureRenderFailsClosedAndDoctor(t *testing.T) {
	d := t.TempDir()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "SOUL.md"), []byte("soul"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, "me"); err != nil {
		t.Fatal(err)
	}
	s, err := Read(d)
	if err != nil {
		t.Fatal(err)
	}
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.Tools, s.Ingress = testTools("exposure"), testIngress("exposure")
	s.SpaceDir = d
	if err = WriteSpace(d, s); err != nil {
		t.Fatal(err)
	}
	if err = RenderEnvironment(d, root, "prod"); err == nil {
		t.Fatal("render accepted exposure without a config")
	}
	if !strings.Contains(strings.Join(Doctor(s, nil), " "), "connections/exposure/config.yaml") {
		t.Fatal("doctor must flag a missing exposure config")
	}
	writeExposureConfig(t, d)
	for _, issue := range Doctor(s, nil) {
		if strings.Contains(issue, "connections/exposure") {
			t.Fatal("doctor still flags exposure config after creation:", issue)
		}
	}
}
