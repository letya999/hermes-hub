package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/hosting"
	"github.com/letya999/hermes-hub/internal/toolhub"
)

func TestHostingComposeOptInAndMountIsolation(t *testing.T) {
	dir := t.TempDir()
	s := profileSettings("workspace")
	s.User = "alice"
	s.Environment = "dev"
	s.SpaceDir = dir
	off := Compose(s, t.TempDir(), dir)
	if _, ok := off["services"].(M)[hostingName(s)]; ok {
		t.Fatal("hosting enabled by default")
	}
	s.Tools = testTools("hosting", "exposure")
	s.Hosting = HostingSettings{Slots: []string{"page"}, Image: "golang@sha256:" + strings.Repeat("a", 64), Runtime: "runsc"}
	on := Compose(s, t.TempDir(), dir)
	services := on["services"].(M)
	web := services[hostingName(s)].(M)
	controller := services[hostingName(s)+"-ctl"].(M)
	if !strings.HasPrefix(web["user"].(string), "10001:") || len(web["ports"].([]string)) != 0 {
		t.Fatal("public host port or wrong identity")
	}
	for _, mount := range web["volumes"].([]any) {
		m := mount.(M)
		if strings.Contains(m["source"].(string), "docker.sock") || m["target"] == "/state" {
			t.Fatal("hosting web tier received socket or runtime state")
		}
		if m["target"] == "/workspace" && m["read_only"] != true {
			t.Fatal("writable workspace")
		}
	}
	if !strings.HasPrefix(controller["user"].(string), "10001:") || len(controller["ports"].([]string)) != 0 {
		t.Fatal("controller contract")
	}
	runtime := services["hermes-runtime"].(M)
	for _, mount := range runtime["volumes"].([]any) {
		m, ok := mount.(M)
		if ok && m["target"] == "/run/controller.key" {
			t.Fatal("agent received privileged controller credential")
		}
	}
	volume := on["volumes"].(M)["hosting-sites"].(M)
	if volume["name"] != "hermes-hub-sites-alice-dev" {
		t.Fatal("wrong volume binding")
	}
	s.User = "bob"
	s.Environment = "prod"
	other := Compose(s, t.TempDir(), dir)
	if other["volumes"].(M)["hosting-sites"].(M)["name"] == volume["name"] {
		t.Fatal("shared user/env volume")
	}
}
func TestHostingReviewedConfigAndPrivateController(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "runtime"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0700); err != nil {
		t.Fatal(err)
	}
	writeExposureConfig(t, dir)
	s := profileSettings("hosting", "exposure")
	s.User = "alice"
	s.SpaceDir = dir
	s.Hosting = HostingSettings{Slots: []string{"page"}, Image: "golang@sha256:" + strings.Repeat("a", 64), Runtime: "runsc"}
	if issues := hostingIssues(s); len(issues) != 0 {
		t.Fatalf("valid config: %v", issues)
	}
	if err := writeHostingFiles(dir, s); err != nil {
		t.Fatal(err)
	}
	cfg, err := hosting.LoadDeployment(filepath.Join(dir, "generated", "hosting.prod.json"))
	if err != nil || cfg.Owner != "alice" || cfg.Domain != "hub.example.com" {
		t.Fatalf("rendered hosting config: %#v %v", cfg, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "generated", "hosting-controller.prod.json"))
	if err != nil {
		t.Fatal(err)
	}
	var controller toolhub.GenericControllerConfig
	if json.Unmarshal(data, &controller) != nil || !controller.HostingOnly || len(controller.Hosting) != 1 || controller.CLI != nil {
		t.Fatal("hosting controller exposes generic workload policy")
	}
	s.Hosting.Slots = []string{"onboarding"}
	if len(hostingIssues(s)) == 0 {
		t.Fatal("manual endpoint collision accepted")
	}
	s.Hosting.Slots = []string{"page"}
	s.Hosting.Runtime = "runc"
	if len(hostingIssues(s)) == 0 {
		t.Fatal("weak dynamic runtime accepted")
	}
}
