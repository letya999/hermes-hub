package serviceauth

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The auth contract is a leaf shared by control planes: it must never
// import a service it serves — direction is service → serviceauth.
func TestServiceauthNeverImportsServices(t *testing.T) {
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
				t.Fatalf("%s imports %s — serviceauth must stay a leaf", name, path)
			}
		}
	}
}
