// Package archtest makes layer boundaries and dangerous runtime capabilities executable.
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionLayerBoundaries(t *testing.T) {
	root := filepath.Clean("../..")
	prefix := "github.com/letya999/credential-broker/"
	allowed := map[string][]string{
		"identity":    {"internal/strictjson"},
		"contract":    {"identity", "internal/strictjson"},
		"provider":    {"internal/seal", "internal/securefs", "internal/strictjson"},
		"materialize": {"contract", "provider", "identity", "api/v1", "internal/securefs", "internal/strictjson"},
		"broker":      {"identity", "contract", "provider", "materialize", "api/v1", "internal/journal", "internal/seal", "internal/strictjson"},
		"client":      {"identity", "contract", "api/v1"},
		"api/v1":      {"contract", "identity"},
	}
	if e := filepath.WalkDir(root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".tools" || d.Name() == ".work" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pkg := filepath.ToSlash(filepath.Dir(rel))
		f, e := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if e != nil {
			return e
		}
		for _, im := range f.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if p == "os/exec" || p == "unsafe" || p == "plugin" || p == "net/http/pprof" {
				t.Errorf("forbidden production capability %s in %s", p, rel)
			}
			if deps, ok := allowed[pkg]; ok && strings.HasPrefix(p, prefix) {
				target := strings.TrimPrefix(p, prefix)
				found := false
				for _, a := range deps {
					if a == target {
						found = true
					}
				}
				if !found {
					t.Errorf("layer violation %s -> %s", pkg, target)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			assign, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			name, ok := assign.Key.(*ast.Ident)
			if !ok || name.Name != "InsecureSkipVerify" {
				return true
			}
			value, ok := assign.Value.(*ast.Ident)
			if ok && value.Name == "true" {
				t.Errorf("insecure TLS in %s", rel)
			}
			return true
		})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(root, "go.mod"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(raw), "require ") {
		t.Fatal("third-party runtime dependencies require an explicit ADR and gate change")
	}
}
