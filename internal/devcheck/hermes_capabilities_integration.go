//go:build integration

package devcheck

import (
	"bytes"
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/letya999/hermes-hub/internal/stack"
)

//go:embed testdata/hermes_capabilities.py
var hermesCapabilitiesProbe string

// HermesCapabilityContract exercises the pinned upstream in a disposable,
// network-isolated container. Only the model's HTTP responses are synthetic;
// discovery, API agent construction and the conversation dispatcher are real.
func HermesCapabilityContract(ctx context.Context, image string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	id, err := exec.CommandContext(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return fmt.Errorf("capability probe image: %w", err)
	}
	image = strings.TrimSpace(string(id))
	if !strings.HasPrefix(image, "sha256:") || len(image) != 71 {
		return fmt.Errorf("capability probe requires an immutable local image ID")
	}
	var suffix [5]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("hermes-capabilities-%x", suffix)
	manifest, err := stack.ManagedCapabilityInventory()
	if err != nil || manifest.SourcePin != hermesContractPin {
		return fmt.Errorf("managed Hermes inventory does not match source pin")
	}
	config, err := json.Marshal(stack.Config(stack.Settings{CapabilityMode: "managed", Model: "capability-probe", ModelURL: "http://127.0.0.1:1/v1", Timezone: "UTC"}))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "rm", "-f", name).Run()
	}()
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--name", name,
		"--network", "none", "--read-only", "--user", "10001:10001",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--memory", "1g", "--cpus", "2", "--pids-limit", "128",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=128m,mode=1777",
		"--env", "HOME=/tmp/home", "--env", "HERMES_HOME=/tmp/hermes",
		"--env", "HERMES_DISABLE_LAZY_INSTALLS=1", "--env", "PYTHONDONTWRITEBYTECODE=1",
		"--workdir", "/opt/hermes", "--entrypoint", "/opt/hermes/.venv/bin/python",
		image, "-", hermesContractPin, string(config))
	cmd.Stdin = strings.NewReader(hermesCapabilitiesProbe)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, os.Stderr
	fmt.Printf("Hermes capability probe image=%s pin=%s\n", image, hermesContractPin)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("Hermes capability contract: %w: %s", err, tailProbeOutput(output.String()))
	}
	var report struct {
		Pin                    string                     `json:"pin"`
		InventorySHA256        string                     `json:"inventory_sha256"`
		DiskHookImportExecuted bool                       `json:"disk_hook_import_executed"`
		EmptySurfaces          map[string]json.RawMessage `json:"empty_surfaces"`
		Inventory              struct {
			Toolsets map[string]json.RawMessage `json:"toolsets"`
			Entries  map[string]struct {
				Toolset string `json:"toolset"`
			} `json:"entries"`
		} `json:"inventory"`
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "CAPABILITY_REPORT=") {
			err = json.Unmarshal([]byte(strings.TrimPrefix(line, "CAPABILITY_REPORT=")), &report)
			break
		}
	}
	if err != nil || report.Pin != manifest.SourcePin || report.InventorySHA256 != manifest.InventorySHA256 || !report.DiskHookImportExecuted {
		return fmt.Errorf("pinned Hermes capability inventory changed or probe report missing")
	}
	platforms := make([]string, 0, len(report.EmptySurfaces))
	for platform := range report.EmptySurfaces {
		platforms = append(platforms, platform)
	}
	slices.Sort(platforms)
	groups := make([]string, 0, len(report.Inventory.Toolsets)+len(report.Inventory.Entries))
	for group := range report.Inventory.Toolsets {
		groups = append(groups, group)
	}
	for _, entry := range report.Inventory.Entries {
		if entry.Toolset != "" {
			groups = append(groups, entry.Toolset)
		}
	}
	slices.Sort(groups)
	groups = slices.Compact(groups)
	if !slices.Equal(platforms, manifest.Platforms) || !slices.Equal(groups, manifest.DisabledToolsets) {
		return fmt.Errorf("managed Hermes platform or toolset manifest differs from observed registry")
	}
	fmt.Printf("Hermes zero-capability probe passed: %d platforms, %d disabled toolsets, inventory %s\n", len(platforms), len(groups), report.InventorySHA256)
	return nil
}

func tailProbeOutput(output string) string {
	if len(output) > 2000 {
		return output[len(output)-2000:]
	}
	return output
}
