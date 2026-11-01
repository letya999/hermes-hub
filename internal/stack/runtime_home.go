package stack

import "path/filepath"

// RuntimeHermesHome is the same owned home selected by the runtime mounts.
func RuntimeHermesHome(dir string, s Settings) string {
	if s.CapabilityMode == "managed" {
		return filepath.Join(dir, "managed", s.Environment, "hermes")
	}
	return filepath.Join(dir, "hermes")
}
