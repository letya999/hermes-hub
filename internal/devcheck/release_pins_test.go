package devcheck

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Release pin guard: image tags, the CLI banner and the probe identity all
// carry the release version literally. When the version advances this test
// lists every stale site so the bump cannot land half-done. Historical
// mentions like "v0.4.0 removed ..." or bare compatibility_release fixtures
// do not match the pinned shapes below.
func TestReleasePinsAreCurrent(t *testing.T) {
	stale := []*regexp.Regexp{
		regexp.MustCompile(`hermes-hub(-[\w]+)?:0\.4\.0`),
		regexp.MustCompile(`:0\.4\.0-`),
		regexp.MustCompile(`hubctl 0\.4\.0`),
		regexp.MustCompile(`"version":\s*"0\.4\.0"`),
	}
	roots := []string{"../../cmd", "../../internal", "../../docs", "../../docker", "../../scripts", "../../justfile"}
	var hits []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			base := filepath.Base(path)
			ext := filepath.Ext(base)
			if ext != ".go" && ext != ".md" && ext != ".ps1" && !strings.HasPrefix(base, "Dockerfile") {
				return nil
			}
			body, err := os.ReadFile(path) // #nosec G703 -- test walks the checked-in tree
			if err != nil {
				return err
			}
			for _, re := range stale {
				if m := re.FindString(string(body)); m != "" {
					hits = append(hits, fmt.Sprintf("%s: %s", path, m))
					break
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(hits) > 0 {
		t.Fatalf("stale 0.4.0 release pins:\n%s", strings.Join(hits, "\n"))
	}
}
