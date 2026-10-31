package devcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoverageRejectsMissingLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte("mode: count\n6.52 2 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := parseCoverage(path); err == nil {
		t.Fatal("malformed source location accepted")
	}
}
