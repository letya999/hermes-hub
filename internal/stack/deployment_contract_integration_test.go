//go:build integration

package stack

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Uses the actual Compose loader, without starting containers or using accounts.
func TestDeploymentConfigurationContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"dev", "prod"} {
		for _, mode := range []string{"static", "supervisor"} {
			for _, capability := range []string{"legacy", "managed"} {
				t.Run(environment+"/"+mode+"/"+capability, func(t *testing.T) {
					dir := t.TempDir()
					if err := InitEnvironment(dir, "contract", environment); err != nil {
						t.Fatal(err)
					}
					s, err := Read(dir)
					if err != nil {
						t.Fatal(err)
					}
					s.Model, s.ModelURL = "synthetic", "http://model.invalid/v1"
					s.CapabilityMode = ""
					s.Tools = testTools("workspace")
					if capability == "managed" {
						s.CapabilityMode = capability
						s.CapabilityProfileID, s.CapabilityGeneration = "contract-default", 1
						s.ModelURL = "http://model-relay:8318/v1"
						s.Tools = map[string]ToolEntry{}
					}
					if err := WriteSpace(dir, s); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "secrets."+environment+".env"), []byte("OPENAI_API_KEY=synthetic-contract-only\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if mode == "supervisor" {
						selection := ExecutionSelection{Schema: 1, User: "contract", Environment: environment, Mode: mode, SupervisorURL: "http://host.docker.internal:8876", NativeCron: "disabled", CompatibilityRelease: "0.3.0"}
						data, err := json.Marshal(selection)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(dir, ExecutionPath(environment)), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					renderErr := RenderEnvironment(dir, root, environment)
					if capability == "managed" && mode == "static" {
						if renderErr == nil || !strings.Contains(renderErr.Error(), "managed runtime requires supervisor") {
							t.Fatalf("unproven static managed runtime accepted: %v", renderErr)
						}
						return
					}
					if err := renderErr; err != nil {
						t.Fatal(err)
					}
					s.Environment, s.ExecutionMode = environment, mode
					if mode == "supervisor" {
						s.SupervisorURL = "http://host.docker.internal:8876"
					}
					body, err := yaml.Marshal(Compose(s, root, dir))
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, "contract.yaml")
					if err := os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
					validate := func(auth string) ([]byte, error) {
						cmd := exec.Command("docker", "compose", "-f", path, "config", "--quiet")
						// Do not inherit account credentials or auth from the developer.
						for _, entry := range os.Environ() {
							if !strings.HasPrefix(entry, "HUB_") && !strings.HasPrefix(entry, "COMPOSE_") {
								cmd.Env = append(cmd.Env, entry)
							}
						}
						cmd.Env = append(cmd.Env, "HUB_SUPERVISOR_AUTH="+auth)
						return cmd.CombinedOutput()
					}
					if output, err := validate("synthetic-control-only"); err != nil {
						t.Fatalf("Compose contract invalid: %s (%v)", output, err)
					}
					output, err := validate("")
					if mode == "supervisor" {
						if err == nil || !strings.Contains(string(output), "HUB_SUPERVISOR_AUTH") {
							t.Fatalf("missing auth did not fail with actionable diagnostic: %s (%v)", output, err)
						}
					} else if err != nil {
						t.Fatalf("static mode requires unused supervisor credentials: %s (%v)", output, err)
					}
				})
			}
		}
	}
}
