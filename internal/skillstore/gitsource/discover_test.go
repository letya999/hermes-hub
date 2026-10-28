package gitsource

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/letya999/hermes-hub/internal/skillstore"
)

func tree(paths ...string) skillstore.Tree {
	t := skillstore.Tree{}
	for _, p := range paths {
		t[p] = skillstore.File{Data: []byte("# x")}
	}
	return t
}

func TestDiscoverSkillsFindsConventionsAndNested(t *testing.T) {
	got := DiscoverSkills(tree(
		"skills/pdf/SKILL.md",
		".agents/skills/docx/SKILL.md",
		"deep/a/b/c/d/e/SKILL.md",   // depth 6 — admitted
		"deep/a/b/c/d/e/f/SKILL.md", // depth 7 — beyond the bound
		".git/hooks/SKILL.md",       // skipped
		"node_modules/x/SKILL.md",   // skipped
		"README.md",
	), "")
	var dirs []string
	for _, c := range got {
		dirs = append(dirs, c.Path)
	}
	want := map[string]bool{"skills/pdf": true, ".agents/skills/docx": true, "deep/a/b/c/d/e": true}
	if len(dirs) != len(want) {
		t.Fatalf("candidates %v", dirs)
	}
	for _, d := range dirs {
		if !want[d] {
			t.Fatalf("unexpected candidate %q", d)
		}
	}
}

// R10.5: convention dirs (.agents/skills, skills, .claude/skills) rank
// before arbitrary directories; each group stays lexicographically sorted.
func TestDiscoverSkillsRanksConventionsFirst(t *testing.T) {
	got := DiscoverSkills(tree(
		"examples/aaa/SKILL.md",
		"skills/bbb/SKILL.md",
		".claude/skills/ccc/SKILL.md",
		".agents/skills/ddd/SKILL.md",
		"vendor/eee/SKILL.md",
	), "")
	var dirs []string
	for _, c := range got {
		dirs = append(dirs, c.Path)
	}
	want := []string{".agents/skills/ddd", ".claude/skills/ccc", "skills/bbb", "examples/aaa", "vendor/eee"}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("ranking %v want %v", dirs, want)
	}
}

// R10.5: the walk is bounded at 2000 directories — a pathological tree
// truncates deterministically at the lexical cutoff instead of scanning on.
func TestDiscoverSkillsBoundsDirectoryCount(t *testing.T) {
	var paths []string
	for i := 0; i < 2100; i++ {
		paths = append(paths, fmt.Sprintf("d%04d/SKILL.md", i))
	}
	got := DiscoverSkills(tree(paths...), "")
	if len(got) != 2000 || got[0].Path != "d0000" || got[len(got)-1].Path != "d1999" {
		t.Fatalf("bound %d first %v last %v", len(got), got[0].Path, got[len(got)-1].Path)
	}
}

// R10.5: discovery runs "under path" — a subtree argument restricts the walk.
func TestDiscoverSkillsHonorsSubdir(t *testing.T) {
	got := DiscoverSkills(tree(
		"plugins/pdf/SKILL.md",
		"plugins/docx/SKILL.md",
		"other/x/SKILL.md",
	), "plugins")
	if len(got) != 2 || got[0].Path != "plugins/docx" || got[1].Path != "plugins/pdf" {
		t.Fatalf("subdir discovery %v", got)
	}
}
