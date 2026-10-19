package commodel

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The domain model is the shared bottom of the dependency graph: it must
// never import a service or the provider seam.
func TestCommodelNeverImportsServices(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "internal/communication") || strings.Contains(path, "internal/toolhub") || strings.Contains(path, "internal/commops") || strings.Contains(path, "internal/channel") {
				t.Fatalf("%s imports %s — the domain model must stay a leaf", name, path)
			}
		}
	}
}
