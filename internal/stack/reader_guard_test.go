package stack_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SPEC-0092 R8.1 / CHG-0262 S8.2: runtime callers use the explicit
// ReadEffective/ReadEffectiveSpace APIs. ReadEnvironment remains a compatibility
// alias for stack and migration callers and must not regain runtime callers.
func TestReadEnvironmentMigrationOnly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"internal/stack":     true, // definition + legacy-tail implementation
		"internal/migration": true, // pre-migration file reads
	}
	var violators []string
	walk := func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "vendor" || name == ".work" || name == ".local" || name == ".tmpgen" || name == "spaces" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkg := filepath.Dir(rel)
		if allowed[filepath.ToSlash(pkg)] {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "ReadEnvironment(") {
			violators = append(violators, rel)
		}
		return nil
	}
	if err := filepath.WalkDir(root, walk); err != nil {
		t.Fatal(err)
	}
	if len(violators) > 0 {
		t.Fatalf("ReadEnvironment called outside internal/{stack,migration}: %v", violators)
	}
}
