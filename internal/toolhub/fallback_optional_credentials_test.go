package toolhub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/letya999/hermes-hub/internal/toolhub/api"
)

func TestFallbackCreatesBridgeWithoutOptionalEnvFile(t *testing.T) {
	d, root, paths := genericDefinition(t)
	parts := strings.Split(paths, "\x00")
	d.Credentials = []api.CredentialInput{{Name: "TOKEN"}}
	d.Source.Command = "server"
	c, err := newGenericController(GenericControllerConfig{StateRoot: root, ToolHiveBinary: parts[0], SeccompProfile: parts[1], DockerFallback: true, BridgeBinary: parts[0], Definition: d, MaxActive: 1, IdleTTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	plan := genericPlan(d)
	plan.WorkspacePath = t.TempDir()
	reached := false
	stop := errors.New("bridge create observed")
	c.commandEnv = func(context.Context, map[string]string, string, ...string) ([]byte, error) { return nil, nil }
	c.command = func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != "docker" || len(args) == 0 {
			return nil, nil
		}
		switch args[0] {
		case "context":
			return []byte("unix:///var/run/docker.sock"), nil
		case "info":
			return []byte("linux"), nil
		case "inspect":
			return []byte(`{"hermes-generic-workload":{"IPAddress":"172.20.0.2"}}`), nil
		case "create":
			if api.Contains(args, "/hermes-bridge/hubctl") {
				reached = true
				if api.Contains(args, "--env-file") {
					t.Error("bridge references an absent optional env file")
				}
				return nil, stop
			}
		}
		return nil, nil
	}
	_, err = c.startDockerRemoteFallback(t.Context(), plan)
	if !reached || !errors.Is(err, stop) {
		t.Fatalf("bridge create not reached: reached=%t err=%v", reached, err)
	}
}

func TestMaterializeRuntimeCredentialsMissingOptionalFile(t *testing.T) {
	workspace := t.TempDir()
	definition := api.ToolDefinition{Credentials: []api.CredentialInput{{Name: "API_TOKEN"}}}
	env, mounts, err := materializeRuntimeCredentials(workspace, definition)
	if err != nil || env != "" || len(mounts) != 0 {
		t.Fatalf("empty optional delivery must need no env file: env=%q mounts=%v err=%v", env, mounts, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "credentials.env")); !os.IsNotExist(err) {
		t.Fatal("empty optional delivery must not create an env file")
	}
	definition.Credentials[0].Required = true
	if _, _, err := materializeRuntimeCredentials(workspace, definition); err == nil {
		t.Fatal("missing required credential accepted")
	}
	definition.Credentials[0].Required = false
	if _, _, err := materializeRuntimeCredentials("", definition); err == nil {
		t.Fatal("empty workspace accepted")
	}
	if err := os.WriteFile(filepath.Join(workspace, "credentials.env"), []byte("UNDECLARED=value"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := materializeRuntimeCredentials(workspace, definition); err == nil {
		t.Fatal("invalid existing env file accepted")
	}
}
