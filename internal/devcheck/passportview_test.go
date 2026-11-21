package devcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoPassportviewImportsOutsideSpawnController keeps the deleted v0
// passport reader package unreferenced: post-S7d only spawn-controller may
// even mention it (historical comments); every consumer reads the
// contracts/spawn/v1 effective/policy endpoints instead.
func TestNoPassportviewImportsOutsideSpawnController(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				switch info.Name() {
				case ".git", ".work", "vendor", "node_modules":
					return filepath.SkipDir
				}
			}
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "services/spawn-controller/") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "\"github.com/letya999/hermes-hub/internal/passportview\"") {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("internal/passportview referenced outside spawn-controller: %s", strings.Join(offenders, ", "))
	}
}
