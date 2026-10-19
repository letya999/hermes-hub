package channel

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The provider seam never imports the gateway it serves: adapters reach the
// gateway only through their injected Deps — direction is
// communication → channel, never the reverse. This is what lets provider
// code change without touching core routines.
func TestChannelNeverImportsGateway(t *testing.T) {
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
			if strings.Contains(path, "internal/communication") || strings.Contains(path, "internal/toolhub") || strings.Contains(path, "internal/commops") {
				t.Fatalf("%s imports %s — provider seam must not reach services", name, path)
			}
		}
	}
}
