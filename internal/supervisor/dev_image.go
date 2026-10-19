package supervisor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// devImage resolves an operator-pinned dev overlay image: the pin file
// under generated/ must name exactly the image dev-rebuild produced, so a
// forged marker can never redirect a spawn to an arbitrary image.
func devImage(root, file, service, user, env, fallback string) (string, error) {
	if env != "dev" {
		return fallback, nil
	}
	value, err := os.ReadFile(filepath.Join(root, "generated", file))
	if os.IsNotExist(err) {
		return fallback, nil
	}
	if err != nil {
		return "", err
	}
	image := strings.TrimSpace(string(value))
	if image != "hermes-hub-dev-"+user+"-"+service+":local" {
		return "", fmt.Errorf("invalid dev %s image selection", service)
	}
	return image, nil
}

// devScriptImage resolves the image disposable script containers spawn
// from. The helper entrypoint is communication-hub, so the pin written by
// dev-rebuild communication-hub steers script probes to the same code the
// gateway itself runs; prod always falls back to the core image.
func devScriptImage(root, user, env, fallback string) (string, error) {
	return devImage(root, "dev-script-image", "communication-hub", user, env, fallback)
}

func devRuntimeImage(root, user, env, fallback string) (string, error) {
	return devImage(root, "dev-runtime-image", "hermes-runtime", user, env, fallback)
}
