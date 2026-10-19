package routines

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Routines semantics are a leaf: the package must never import services or
// the provider seam — direction is communication → routines → commodel.
func TestRoutinesNeverImportsServices(t *testing.T) {
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
				t.Fatalf("%s imports %s — routines semantics must stay a leaf", name, path)
			}
		}
	}
}
