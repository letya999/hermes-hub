package gitsource

import (
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

// Candidate is one discovered skill directory inside an exported tree.
type Candidate struct {
	Path string `json:"path"` // repo-relative directory containing SKILL.md
	Name string `json:"name"`
}

// maxDiscoverDepth bounds the repo-relative depth a SKILL.md may sit at;
// maxDiscoverDirs bounds the directories the walk may collect (R10.5).
const (
	maxDiscoverDepth = 6
	maxDiscoverDirs  = 2000
)

// DiscoverSkills returns candidate skill directories (R10.5). A non-empty
// subdir restricts the walk to that repo-relative subtree.
func DiscoverSkills(tree skillstore.Tree, subdir string) []Candidate {
	var out []Candidate
	prefix := ""
	if subdir != "" {
		prefix = strings.TrimSuffix(subdir, "/") + "/"
	}
	for _, file := range slices.Sorted(maps.Keys(tree)) {
		if len(out) >= maxDiscoverDirs {
			break
		}
		if !strings.HasSuffix(file, "/SKILL.md") || (prefix != "" && !strings.HasPrefix(file, prefix)) {
			continue
		}
		dir := strings.TrimSuffix(file, "/SKILL.md")
		depth := strings.Count(dir, "/") + 1
		skip := depth > maxDiscoverDepth
		for _, part := range strings.Split(dir, "/") {
			if part == ".git" || part == "node_modules" {
				skip = true
			}
		}
		if !skip {
			out = append(out, Candidate{Path: dir, Name: path.Base(dir)})
		}
	}
	// Convention dirs rank first (R10.5); each group stays lexical.
	slices.SortFunc(out, func(a, b Candidate) int {
		ca, cb := conventional(a.Path), conventional(b.Path)
		if ca != cb {
			if ca {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return out
}

func conventional(dir string) bool {
	return strings.HasPrefix(dir, "skills/") ||
		strings.HasPrefix(dir, ".agents/skills/") ||
		strings.HasPrefix(dir, ".claude/skills/")
}
