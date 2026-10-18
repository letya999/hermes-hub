package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func devRuntimeImage(root, user, env, fallback string) (string, error) {
	if env != "dev" {
		return fallback, nil
	}
	value, err := os.ReadFile(filepath.Join(root, "generated", "dev-runtime-image"))
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	image := strings.TrimSpace(string(value))
	if image != "hermes-hub-dev-"+user+"-hermes-runtime:local" {
		return "", fmt.Errorf("invalid dev runtime image selection")
	}
	return image, nil
}
