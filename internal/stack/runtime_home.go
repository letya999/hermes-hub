package stack

import "path/filepath"

// RuntimeHermesHome is the same owned home selected by the runtime mounts.
func RuntimeHermesHome(dir string, s Settings) string {
	return filepath.Join(dir, "managed", s.Environment, "hermes")
}
