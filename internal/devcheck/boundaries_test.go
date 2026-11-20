package devcheck

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharedImportBoundary(t *testing.T) {
	for _, imp := range []string{"example.test/hub/shared/auth", "example.test/hub/contracts/task/v1", "example.test/hub/internal/identity", "example.test/hub/services/task/internal/jobs"} {
		t.Run(imp, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "shared", "infra"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/hub\ngo 1.27.4\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "shared", "infra", "fixture.go"), []byte("package infra\nimport _ \""+imp+"\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := SharedBoundaries(root)
			forbidden := strings.Contains(imp, "/internal/") || strings.Contains(imp, "/services/")
			if forbidden != (err != nil) {
				t.Fatalf("import %s: forbidden=%v err=%v", imp, forbidden, err)
			}
		})
	}
	if err := SharedBoundaries("../.."); err != nil {
		t.Fatal(err)
	}
}
