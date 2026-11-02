package migration

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/letya999/hermes-hub/internal/catalog"
	"github.com/letya999/hermes-hub/internal/passport"
)

// sourceFileNames is the fixed layer order under passport-src/ — the same
// order MigratePassport writes and Compile merges: platform ceiling,
// platform template, org ceiling, org template, user.
var sourceFileNames = []string{
	"platform-ceiling.yaml",
	"platform-template.yaml",
	"org-ceiling.yaml",
	"org-template.yaml",
	"user.yaml",
}

// CompileSources assembles compile inputs from the editable authority:
// the five passport-src/ layers (absent org layers are simply empty), the
// toolhub grant ledger and the generated catalog. It performs no writes —
// space apply decides separately whether the compiled revision publishes.
func CompileSources(dir, env string, now time.Time) (passport.Revision, error) {
	var rev passport.Revision
	docs, err := catalog.Generate()
	if err != nil {
		return rev, err
	}
	srcDir := filepath.Join(dir, "passport-src")
	layers := make([]passport.Layer, len(sourceFileNames))
	for i, name := range sourceFileNames {
		b, err := os.ReadFile(filepath.Join(srcDir, name)) // #nosec G304 -- operator-owned source file
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return rev, fmt.Errorf("space apply: read %s: %w", name, err)
		}
		if err := yaml.Unmarshal(b, &layers[i]); err != nil {
			return rev, fmt.Errorf("space apply: parse %s: %w", name, err)
		}
	}
	ledger, _, err := grantLedger(dir, docs, layers)
	if err != nil {
		return rev, err
	}
	space := layers[4].Name
	if space == "" {
		space = filepath.Base(dir)
	}
	return passport.Compile(passport.Inputs{
		Space: space, Environment: env, Now: now,
		PlatformCeiling:  layers[0],
		PlatformTemplate: layers[1],
		OrgCeiling:       layers[2],
		OrgTemplate:      layers[3],
		User:             layers[4],
		Grants:           ledger,
		Catalog:          docs,
	})
}
