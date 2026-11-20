package devcheck

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

// SharedBoundaries keeps infrastructure independent of service/domain code.
// Other service boundaries use the Go compiler's internal visibility rules.
func SharedBoundaries(root string) error {
	module := mainModuleAt(root)
	return filepath.WalkDir(filepath.Join(root, "shared"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			name, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(name, module+"/services/") || strings.HasPrefix(name, module+"/internal/") {
				rel, _ := filepath.Rel(root, path)
				return fmt.Errorf("shared boundary: %s imports %s", filepath.ToSlash(rel), name)
			}
		}
		return nil
	})
}
