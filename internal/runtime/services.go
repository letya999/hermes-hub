package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/stack"
)

const selfServicesFile = "self-services.json"

func loadSelfServices() error {
	features, err := stack.ReadSelfServicesFeatures(filepath.Join(state, selfServicesFile))
	if err != nil {
		return err
	}
	active := map[string]bool{}
	for _, name := range strings.Split(os.Getenv("HUB_FEATURES"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			active[name] = true
		}
	}
	for _, name := range features {
		active[name] = true
	}
	all := make([]string, 0, len(active))
	for name := range active {
		all = append(all, name)
	}
	slices.Sort(all)
	if err := os.Setenv("HUB_ACTIVE_FEATURES", strings.Join(all, ",")); err != nil {
		return err
	}
	if active["hh"] {
		if err := os.Setenv("HUB_HH_ENABLED", "true"); err != nil {
			return err
		}
	}
	return nil
}
