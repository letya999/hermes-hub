package supervisor

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

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

func rolloutPinsPath(spaceDir string) string {
	return filepath.Join(spaceDir, "generated", "image-pins.json")
}

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
	return os.WriteFile(rolloutPinsPath(spaceDir), body, 0o600)
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

// resolveRuntimeImage is the single spawn-time image seam: a matching canary
// wins, then the operator dev pin (existing devRuntimeImage contract), then
// the verified rollout pin, then the flag default. A malformed pins file
// degrades to the flag default with a loud log rather than silently running
// an attacker-selected image; a missing file is normal.
func resolveRuntimeImage(spaceDir, user, contextID, env, flag string) (image, source string, err error) {
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
	dev, err := devRuntimeImage(spaceDir, user, env, "")
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

// imageSourceOf resolves the winning source for the runtime record; a
// resolution failure (e.g. a forged dev pin) leaves the field empty — the
// args builders report the same error when the spawn is attempted.
func imageSourceOf(binding Binding, env, flag string) string {
	_, source, err := resolveRuntimeImage(binding.ContextRoot, binding.UserID, binding.ContextID, env, flag)
	if err != nil {
		return ""
	}
	return source
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
			if !validID(e.Canary.ContextID) {
				return fmt.Errorf("image pins: invalid canary context %q", e.Canary.ContextID)
			}
		}
	}
	return nil
}
