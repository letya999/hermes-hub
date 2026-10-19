package commops

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The ops plane is a leaf: it must never import the gateway it observes —
// the dependency direction is communication → commops, never the reverse.
func TestOpsPackageNeverImportsCommunication(t *testing.T) {
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
			if strings.Contains(path, "internal/communication") || strings.Contains(path, "internal/toolhub") {
				t.Fatalf("%s imports %s — ops plane must stay a leaf", name, path)
			}
		}
	}
}
