package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The runtime image resolution seam (moved from internal/supervisor's
// rollout.go and dev_image.go, CHG-0336 S7c): every managed spawn — and
// every image-pin writer — resolves the workload image through the same
// pins file and the same forge-proof dev overlay contract.

// RolloutPin is one smoke-verified image selection for an environment.
type RolloutPin struct {
	Image    string    `json:"image"`
	SmokedAt time.Time `json:"smoked_at"`
}

// CanaryPin steers one context to a candidate image while the rest of the
// space stays on the verified pin. The canary image is unverified by design.
type CanaryPin struct {
	Image     string    `json:"image"`
	ContextID string    `json:"context_id"`
	SetAt     time.Time `json:"set_at"`
}

// RolloutEnv is the per-environment rollout state: the verified current
// image, the previous verified image for one-command rollback, and an
// optional per-context canary.
type RolloutEnv struct {
	Current  RolloutPin  `json:"current"`
	Previous *RolloutPin `json:"previous,omitempty"`
	Canary   *CanaryPin  `json:"canary,omitempty"`
}

// RolloutPins is <space>/generated/image-pins.json, keyed by environment
// (dev|prod). A missing file is normal; a malformed one fails closed.
type RolloutPins struct {
	Envs map[string]RolloutEnv `json:"envs"`
}

// RolloutPinsPath names the pins file under the space's generated dir;
// exported for the supervisor's compatibility facade and its tests.
func RolloutPinsPath(spaceDir string) string {
	return filepath.Join(spaceDir, "generated", "image-pins.json")
}

func rolloutPinsPath(spaceDir string) string { return RolloutPinsPath(spaceDir) }

func ReadRolloutPins(spaceDir string) (RolloutPins, error) {
	b, err := os.ReadFile(rolloutPinsPath(spaceDir))
	if errors.Is(err, os.ErrNotExist) {
		return RolloutPins{Envs: map[string]RolloutEnv{}}, nil
	}
	if err != nil {
		return RolloutPins{}, err
	}
	var pins RolloutPins
	if err := json.Unmarshal(b, &pins); err != nil {
		return RolloutPins{}, err
	}
	if err := pins.validate(); err != nil {
		return RolloutPins{}, err
	}
	return pins, nil
}

func WriteRolloutPins(spaceDir string, pins RolloutPins) error {
	if err := pins.validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rolloutPinsPath(spaceDir)), 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(pins)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(rolloutPinsPath(spaceDir)), ".image-pins-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), rolloutPinsPath(spaceDir))
}

// validImageRef confines a pin to an OCI-style reference charset so the file
// can never inject whitespace or docker-arg fragments into spawn commands.
func validImageRef(image string) bool {
	for _, c := range image {
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '-' || c == '/' || c == ':' || c == '@' || c == '+'
		if !ok {
			return false
		}
	}
	return true
}

var imagePinID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,255}$`)

// ResolveRuntimeImage is the single spawn-time image seam: a matching
// canary wins, then the operator dev pin (existing devRuntimeImage
// contract), then the verified rollout pin, then the flag default. A
// malformed pins file degrades to the flag default with a loud log rather
// than silently running an attacker-selected image; a missing file is
// normal.
func ResolveRuntimeImage(spaceDir, user, contextID, env, flag string) (image, source string, err error) {
	pins, err := ReadRolloutPins(spaceDir)
	if err != nil {
		// A forged or truncated pins file must never redirect a spawn — fall
		// back to the flag default with a loud log, like a missing file.
		log.Printf("image pins unreadable (%v); using flag default image", err)
		pins = RolloutPins{Envs: map[string]RolloutEnv{}}
	}
	if e, ok := pins.Envs[env]; ok && e.Canary != nil && e.Canary.ContextID == contextID {
		return e.Canary.Image, "canary", nil
	}
	dev, err := DevRuntimeImage(spaceDir, user, env, "")
	if err != nil {
		return "", "", err
	}
	if dev != "" {
		return dev, "dev", nil
	}
	if e, ok := pins.Envs[env]; ok && e.Current.Image != "" {
		return e.Current.Image, "pin", nil
	}
	return flag, "default", nil
}

// DevImage resolves an operator-pinned dev overlay image: the pin file
// under generated/ must name exactly the image dev-rebuild produced, so a
// forged marker can never redirect a spawn to an arbitrary image.
func DevImage(root, file, service, user, env, fallback string) (string, error) {
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

// DevScriptImage resolves the image disposable script containers spawn
// from. The helper entrypoint is communication-hub, so the pin written by
// dev-rebuild communication-hub steers script probes to the same code the
// gateway itself runs; otherwise the operator-selected script core image runs.
func DevScriptImage(root, user, env, fallback string) (string, error) {
	return DevImage(root, "dev-script-image", "communication-hub", user, env, fallback)
}

// DevRuntimeImage resolves the managed runtime's dev overlay pin.
func DevRuntimeImage(root, user, env, fallback string) (string, error) {
	return DevImage(root, "dev-runtime-image", "hermes-runtime", user, env, fallback)
}

func (p RolloutPins) validate() error {
	for env, e := range p.Envs {
		if env != "dev" && env != "prod" {
			return fmt.Errorf("image pins: unknown environment %q", env)
		}
		if e.Current.Image != "" && !validImageRef(e.Current.Image) {
			return fmt.Errorf("image pins: invalid image ref %q", e.Current.Image)
		}
		if e.Previous != nil && e.Previous.Image != "" && !validImageRef(e.Previous.Image) {
			return fmt.Errorf("image pins: invalid image ref %q", e.Previous.Image)
		}
		if e.Canary != nil {
			if e.Canary.Image != "" && !validImageRef(e.Canary.Image) {
				return fmt.Errorf("image pins: invalid canary image ref %q", e.Canary.Image)
			}
			if !imagePinID.MatchString(e.Canary.ContextID) {
				return fmt.Errorf("image pins: invalid canary context %q", e.Canary.ContextID)
			}
		}
	}
	return nil
}
