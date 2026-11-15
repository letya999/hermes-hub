package devcheck

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// M32Lifecycle runs the isolated tagged tests from the repository root.
func M32Lifecycle(ctx context.Context, image, environment string) error {
	if image == "" || environment != "dev" && environment != "prod" {
		return errors.New("M32 acceptance requires an image and dev or prod environment")
	}
	command := exec.CommandContext(ctx, "go", "test", "-tags", "integration", "-timeout", "20m", "-count=1", "-run", "^TestM32|^TestDockerM10ScriptMemoryIsolation$", "-v", "./cmd/toolhub", "./internal/devcheck", "./internal/supervisor")
	command.Env = append(os.Environ(), "HUB_M32_IMAGE="+image, "HUB_M32_SCRIPT_IMAGE=hermes-hub:test-core", "HUB_SCRIPT_IMAGE=hermes-hub:test-core", "HUB_M32_ENVIRONMENT="+environment)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}
