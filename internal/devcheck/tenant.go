package devcheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TenantCoverage refuses a package that owns tenant state but carries no
// TestTenantInvariants: `go test -run` would otherwise pass it silently as
// "no tests to run". A directory owns tenant state when it holds migration
// .sql files or its production Go calls into the scoped store surface.
func TenantCoverage(root string) error {
	var missing []string
	for _, top := range []string{"cmd", "contracts", "internal", "services", "shared"} {
		base := filepath.Join(root, top)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			if covered, owned, werr := tenantPackage(root, path); werr != nil {
				return werr
			} else if owned && !covered {
				rel, _ := filepath.Rel(root, path)
				missing = append(missing, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("devcheck: tenant surface without TestTenantInvariants: %s", strings.Join(missing, ", "))
	}
	return nil
}

// tenantPackage reports whether the directory owns tenant state and whether
// one of its test files defines TestTenantInvariants. The storetest package
// is the runner itself, not an owner.
func tenantPackage(root, dir string) (covered, owned bool, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, false, err
	}
	var goFiles, testFiles []string
	owned = false
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
		case strings.HasSuffix(name, ".sql"):
			owned = true
		case strings.HasSuffix(name, "_test.go"):
			testFiles = append(testFiles, name)
		case strings.HasSuffix(name, ".go"):
			goFiles = append(goFiles, name)
		}
	}
	for _, name := range goFiles {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return false, false, err
		}
		text := string(body)
		// Only the shared store's tenant API counts: credstore/skillstore
		// have Open methods of their own, and .Scoped( alone matches
		// unrelated helpers.
		if !strings.Contains(text, `"github.com/letya999/hermes-hub/shared/store"`) {
			continue
		}
		if strings.Contains(text, ".Scoped(") || strings.Contains(text, "store.Open(") || strings.Contains(text, "store.ReadTx(") || strings.Contains(text, "store.Tx(") {
			owned = true
		}
	}
	if !owned {
		return false, false, nil
	}
	if filepath.Base(dir) == "storetest" {
		return true, true, nil
	}
	for _, name := range testFiles {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return false, false, err
		}
		if strings.Contains(string(body), "func TestTenantInvariants(") {
			return true, true, nil
		}
	}
	return false, true, nil
}
