package skillstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentitySkillCeilingSurvivesRebuildAndRemovesStaleTrees(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	for _, name := range []string{"reader", "writer"} {
		if err := os.MkdirAll(filepath.Join(source, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name, SkillFile), []byte("---\nname: "+name+"\ndescription: Fixture skill\n---\nFixture.\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources := []Source{{Name: "user", Dir: source}}
	dest, meta := filepath.Join(root, "view"), filepath.Join(root, "meta")
	if _, err := Build(dest, sources); err != nil {
		t.Fatal(err)
	}
	if err := WriteViewMeta(meta, ViewMeta{SkillCeiling: []string{"reader"}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		idx, err := RebuildEffective(dest, sources, meta)
		if err != nil || len(idx.Skills) != 1 || idx.Skills[0].Name != "reader" {
			t.Fatalf("identity ceiling lost: %+v %v", idx, err)
		}
		if _, err := os.Stat(filepath.Join(dest, "writer")); !os.IsNotExist(err) {
			t.Fatal("denied skill tree remained readable")
		}
	}
	if err := WriteViewMeta(meta, ViewMeta{SkillCeiling: []string{}}); err != nil {
		t.Fatal(err)
	}
	idx, err := RebuildEffective(dest, sources, meta)
	if err != nil || len(idx.Skills) != 0 {
		t.Fatal("empty skill ceiling inherited skills")
	}
	if _, err := os.Stat(filepath.Join(dest, "reader")); !os.IsNotExist(err) {
		t.Fatal("empty ceiling retained old tree")
	}
}
