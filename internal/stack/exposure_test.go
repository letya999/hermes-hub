package stack

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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
	tools, ingress, err := featureTools([]string{"exposure"})
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
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	writeExposureConfig(t, d)
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.Tools, s.Ingress = testTools("exposure"), testIngress("exposure")
	if err = saveSpace(t, d, s); err != nil {
		t.Fatal(err)
	}
	if err = RenderEnvironment(d, root, "prod"); err != nil {
		t.Fatal(err)
	}
	// Managed runtimes are supervisor-spawned: the exposure state dir is a
	// read-only mount in the RuntimeService spawn contract, not the compose
	// file.
	found := false
	for _, v := range RuntimeService(s, root, d)["volumes"].([]any) {
		m := v.(M)
		if m["target"] == "/state/exposure" {
			if m["read_only"] != true {
				t.Fatal("exposure config mount must be read-only")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("exposure config mount missing from RuntimeService")
	}
	// The HUB_EXPOSURE_CONFIG env lives on the unmanaged `hub` stdio server —
	// managed execution reaches the exposure config through ToolHub.
	unmanaged := s
	unmanaged.CapabilityMode = ""
	hub := Config(unmanaged)["mcp_servers"].(M)["hub"].(M)
	env := hub["env"].(M)
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
	publishManagedRevision(t, d, "me", "r-0000000000000001")
	s, err := ReadEffective(d, "prod")
	if err != nil {
		t.Fatal(err)
	}
	s.Model = "test"
	s.ModelURL = "http://host.docker.internal:8317/v1"
	s.Tools, s.Ingress = testTools("exposure"), testIngress("exposure")
	s.SpaceDir = d
	if err = saveSpace(t, d, s); err != nil {
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
