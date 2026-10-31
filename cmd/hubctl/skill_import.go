package main

// Local package import for `skill install --from`: a directory, a zip
// archive or a standalone markdown file all normalize through the shared
// skillstore import helpers — dot plumbing dropped, the single skill root
// selected, the name resolved from frontmatter before the path basename.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

func skillLocalImport(path, nameOverride string) (string, skillstore.Tree, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, nil, err
	}
	var pkg skillstore.Tree
	var dropped []string
	fallback := filepath.Base(path)
	switch {
	case info.IsDir():
		root, rerr := os.OpenRoot(path)
		if rerr != nil {
			return "", nil, nil, rerr
		}
		defer root.Close()
		pkg, dropped, err = skillstore.ImportTree(root.FS())
	case strings.HasSuffix(strings.ToLower(path), ".zip"):
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			fallback = strings.TrimSuffix(fallback, filepath.Ext(fallback))
			pkg, dropped, err = skillstore.ImportZip(data)
		}
	default:
		var data []byte
		data, err = os.ReadFile(path)
		if err == nil {
			pkg = skillstore.FileTree(data)
			fallback = strings.TrimSuffix(fallback, filepath.Ext(fallback))
			if fallback == "" || fallback == "SKILL" {
				fallback = filepath.Base(filepath.Dir(path))
			}
		}
	}
	if err != nil {
		return "", nil, nil, err
	}
	roots := skillstore.SkillRoots(pkg)
	if len(roots) == 0 {
		return "", nil, nil, fmt.Errorf("%w: no SKILL.md found under %s", skillstore.ErrInvalid, path)
	}
	tree := skillstore.SelectTree(pkg, roots[0])
	if roots[0] != "" {
		fallback = filepath.Base(roots[0])
	}
	name := strings.TrimSpace(nameOverride)
	if name == "" {
		name = skillstore.NameFromTree(tree, fallback)
	}
	return name, tree, skillstore.DropNotes(dropped), nil
}
